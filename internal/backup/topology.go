package backup

import (
	"fmt"
	"sort"
)

type AgentTopology struct {
	NodeID         string
	Hostname       string
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

const (
	capacityScoreScale  = 10.0
	inboundPenaltyScale = 5.0
)

// selectTargets walks the sorted node ring after source, then overlays
// failure-domain anti-affinity and unequal capacity weights. Equal-capacity
// clusters therefore get a deterministic ring instead of lexicographic pairs.
func selectTargets(source AgentTopology, eligible []AgentTopology, count int, inboundLoad map[string]int) []string {
	if count <= 0 {
		return []string{}
	}
	successors := ringSuccessors(source, eligible)
	if len(successors) == 0 {
		return []string{}
	}
	if count > len(successors) {
		count = len(successors)
	}

	ringIdx := successorIndex(successors)
	preferred, fallback := partitionByAffinity(source, successors)
	capacityPool := preferred
	if len(preferred) < count {
		capacityPool = append(append([]AgentTopology{}, preferred...), fallback...)
	}
	if capacityWeightsDiffer(capacityPool) {
		preferred = rankByCapacity(preferred, inboundLoad, ringIdx)
		fallback = rankByCapacity(fallback, inboundLoad, ringIdx)
	}

	chosen := append(append([]AgentTopology{}, preferred...), fallback...)
	if len(chosen) > count {
		chosen = chosen[:count]
	}
	sort.SliceStable(chosen, func(i, j int) bool {
		return ringIdx[chosen[i].NodeID] < ringIdx[chosen[j].NodeID]
	})

	selected := make([]string, 0, len(chosen))
	for _, node := range chosen {
		selected = append(selected, node.NodeID)
		inboundLoad[node.NodeID]++
	}
	return selected
}

func ringSuccessors(source AgentTopology, eligible []AgentTopology) []AgentTopology {
	start := -1
	for i, node := range eligible {
		if node.NodeID == source.NodeID {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	out := make([]AgentTopology, 0, len(eligible)-1)
	for step := 1; step < len(eligible); step++ {
		node := eligible[(start+step)%len(eligible)]
		if node.NodeID == source.NodeID {
			continue
		}
		out = append(out, node)
	}
	return out
}

func successorIndex(successors []AgentTopology) map[string]int {
	idx := make(map[string]int, len(successors))
	for i, node := range successors {
		idx[node.NodeID] = i
	}
	return idx
}

func affinityRank(source, candidate AgentTopology) int {
	if source.Zone != "" && candidate.Zone != "" && source.Zone != candidate.Zone {
		return 3
	}
	if source.Rack != "" && candidate.Rack != "" && source.Rack != candidate.Rack {
		return 2
	}
	if source.Host != "" && candidate.Host != "" && source.Host != candidate.Host {
		return 1
	}
	return 0
}

func partitionByAffinity(source AgentTopology, successors []AgentTopology) (preferred, fallback []AgentTopology) {
	best := 0
	for _, candidate := range successors {
		if rank := affinityRank(source, candidate); rank > best {
			best = rank
		}
	}
	if best == 0 {
		return successors, nil
	}
	for _, candidate := range successors {
		if affinityRank(source, candidate) == best {
			preferred = append(preferred, candidate)
		} else {
			fallback = append(fallback, candidate)
		}
	}
	return preferred, fallback
}

func capacityWeightsDiffer(nodes []AgentTopology) bool {
	if len(nodes) < 2 {
		return false
	}
	weight := nodes[0].CapacityWeight
	for _, node := range nodes[1:] {
		if node.CapacityWeight != weight {
			return true
		}
	}
	return false
}

func capacityScore(node AgentTopology, inbound int) float64 {
	return node.CapacityWeight*capacityScoreScale - float64(inbound)*inboundPenaltyScale
}

func rankByCapacity(pool []AgentTopology, inboundLoad map[string]int, ringIdx map[string]int) []AgentTopology {
	ranked := append([]AgentTopology{}, pool...)
	sort.SliceStable(ranked, func(i, j int) bool {
		si := capacityScore(ranked[i], inboundLoad[ranked[i].NodeID])
		sj := capacityScore(ranked[j], inboundLoad[ranked[j].NodeID])
		if si != sj {
			return si > sj
		}
		return ringIdx[ranked[i].NodeID] < ringIdx[ranked[j].NodeID]
	})
	return ranked
}
