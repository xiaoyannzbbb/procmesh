package backup

import (
	"fmt"
	"sort"
)

type AgentTopology struct {
	NodeID         string
	Host           string
	Rack           string
	Zone           string
	CapacityWeight float64
	Admitted       bool
	Alive          bool
}

type RouteDraft struct {
	SourceNodeID  string
	TargetNodeIDs []string
	Warnings      []string
}

type TopologyConstraints struct {
	// Future: MinZoneDiversity, MinRackDiversity, etc.
}

type RouteDraftResult struct {
	Routes   []RouteDraft
	Warnings []string
}

func GenerateRoutes(nodes []AgentTopology, replicaFactor int, constraints TopologyConstraints) (RouteDraftResult, error) {
	sourceNodeIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.Admitted {
			sourceNodeIDs = append(sourceNodeIDs, node.NodeID)
		}
	}
	return GenerateRoutesForSources(nodes, sourceNodeIDs, replicaFactor, constraints)
}

// GenerateRoutesForSources creates routes only for selected admitted sources while
// retaining every admitted node as a possible target for those routes.
func GenerateRoutesForSources(nodes []AgentTopology, sourceNodeIDs []string, replicaFactor int, constraints TopologyConstraints) (RouteDraftResult, error) {
	// Admission defines the durable topology; liveness only affects warnings.
	eligible := make([]AgentTopology, 0, len(nodes))
	for _, n := range nodes {
		if n.Admitted {
			eligible = append(eligible, n)
		}
	}

	// Sort by NodeID for deterministic output
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].NodeID < eligible[j].NodeID
	})
	eligibleByID := make(map[string]AgentTopology, len(eligible))
	for _, node := range eligible {
		eligibleByID[node.NodeID] = node
	}
	sources := make([]AgentTopology, 0, len(sourceNodeIDs))
	seenSources := make(map[string]struct{}, len(sourceNodeIDs))
	for _, sourceNodeID := range sourceNodeIDs {
		if _, seen := seenSources[sourceNodeID]; seen {
			return RouteDraftResult{}, fmt.Errorf("duplicate source node %q", sourceNodeID)
		}
		source, ok := eligibleByID[sourceNodeID]
		if !ok {
			return RouteDraftResult{}, fmt.Errorf("source node %q is not admitted", sourceNodeID)
		}
		seenSources[sourceNodeID] = struct{}{}
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].NodeID < sources[j].NodeID })

	result := RouteDraftResult{
		Routes:   make([]RouteDraft, 0, len(sources)),
		Warnings: []string{},
	}
	for _, node := range eligible {
		if !node.Alive {
			result.Warnings = append(result.Warnings, "admitted-node-offline:"+node.NodeID)
		}
	}

	// N=1: single node, no replication possible
	if len(eligible) == 1 {
		result.Warnings = append(result.Warnings, "single-node-no-replica")
		return result, nil
	}

	// Check if nodes have topology labels
	hasTopology := false
	for _, n := range eligible {
		if n.Zone != "" || n.Rack != "" || n.Host != "" {
			hasTopology = true
			break
		}
	}
	if !hasTopology {
		result.Warnings = append(result.Warnings, "no-topology-labels")
	}

	// Check if we can satisfy replica factor
	maxTargets := len(eligible) - 1 // each source can't target itself
	actualReplicas := replicaFactor
	if replicaFactor > maxTargets {
		actualReplicas = maxTargets
		result.Warnings = append(result.Warnings, "insufficient-candidates-degraded")
	}

	// Track global inbound load across all sources for capacity balancing
	inboundLoad := make(map[string]int)

	// Generate routes for each source
	for _, source := range sources {
		targets := selectTargets(source, eligible, actualReplicas, inboundLoad)
		result.Routes = append(result.Routes, RouteDraft{
			SourceNodeID:  source.NodeID,
			TargetNodeIDs: targets,
			Warnings:      []string{},
		})
	}

	return result, nil
}

func selectTargets(source AgentTopology, eligible []AgentTopology, count int, inboundLoad map[string]int) []string {
	if count == 0 {
		return []string{}
	}

	// Build candidate list (exclude source)
	candidates := make([]AgentTopology, 0, len(eligible)-1)
	for _, n := range eligible {
		if n.NodeID != source.NodeID {
			candidates = append(candidates, n)
		}
	}

	if len(candidates) == 0 {
		return []string{}
	}

	// Score candidates by anti-affinity and capacity
	type scoredCandidate struct {
		node  AgentTopology
		score float64
	}
	scored := make([]scoredCandidate, 0, len(candidates))

	for _, c := range candidates {
		score := 0.0

		// Anti-affinity: prefer different zone > rack > host
		if source.Zone != "" && c.Zone != "" && source.Zone != c.Zone {
			score += 100.0
		} else if source.Rack != "" && c.Rack != "" && source.Rack != c.Rack {
			score += 50.0
		} else if source.Host != "" && c.Host != "" && source.Host != c.Host {
			score += 25.0
		}

		// Capacity weight: higher weight = higher score
		if c.CapacityWeight > 0 {
			score += c.CapacityWeight * 10.0
		}

		// Penalize by current inbound load
		score -= float64(inboundLoad[c.NodeID]) * 1000.0

		scored = append(scored, scoredCandidate{node: c, score: score})
	}

	// Sort by score (descending), then by NodeID (ascending) for stability
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].node.NodeID < scored[j].node.NodeID
	})

	// Select top N
	selected := make([]string, 0, count)
	for i := 0; i < count && i < len(scored); i++ {
		selected = append(selected, scored[i].node.NodeID)
		inboundLoad[scored[i].node.NodeID]++
	}

	return selected
}
