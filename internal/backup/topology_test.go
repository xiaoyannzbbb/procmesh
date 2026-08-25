package backup_test

import (
	"reflect"
	"testing"

	"github.com/qleelulu/procmesh/internal/backup"
)

func TestGenerateRoutesForSources_ExplicitSourceUsesAllAdmittedTargets(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "a", Admitted: true},
		{NodeID: "b", Admitted: true},
		{NodeID: "c", Admitted: true},
	}

	got, err := backup.GenerateRoutesForSources(nodes, []string{"a"}, 2, backup.TopologyConstraints{})
	if err != nil {
		t.Fatal(err)
	}
	want := []backup.RouteDraft{{SourceNodeID: "a", TargetNodeIDs: []string{"b", "c"}, Warnings: []string{}}}
	if !reflect.DeepEqual(got.Routes, want) {
		t.Fatalf("routes=%+v, want %+v", got.Routes, want)
	}
}

func TestGenerateRoutes_SingleNode(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 1, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(result.Routes) != 0 {
		t.Fatalf("expected empty routes for single node, got %d routes", len(result.Routes))
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("expected warning for single node, got none")
	}
	found := false
	for _, w := range result.Warnings {
		if w == "single-node-no-replica" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'single-node-no-replica' warning, got %v", result.Warnings)
	}
}

func TestGenerateRoutes_TwoNodes(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", Admitted: true, Alive: true},
		{NodeID: "node-b", Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 1, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(result.Routes))
	}
	// node-a -> node-b, node-b -> node-a
	routeMap := make(map[string][]string)
	for _, r := range result.Routes {
		routeMap[r.SourceNodeID] = r.TargetNodeIDs
	}
	if len(routeMap["node-a"]) != 1 || routeMap["node-a"][0] != "node-b" {
		t.Fatalf("expected node-a -> [node-b], got %v", routeMap["node-a"])
	}
	if len(routeMap["node-b"]) != 1 || routeMap["node-b"][0] != "node-a" {
		t.Fatalf("expected node-b -> [node-a], got %v", routeMap["node-b"])
	}
}

func TestGenerateRoutes_OfflineAdmittedNodeRemainsCandidate(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", Admitted: true, Alive: true},
		{NodeID: "node-b", Admitted: true, Alive: false},
	}
	result, err := backup.GenerateRoutes(nodes, 1, backup.TopologyConstraints{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Routes) != 2 {
		t.Fatalf("routes=%+v, want both admitted nodes", result.Routes)
	}
	foundOfflineWarning := false
	for _, warning := range result.Warnings {
		if warning == "admitted-node-offline:node-b" {
			foundOfflineWarning = true
		}
	}
	if !foundOfflineWarning {
		t.Fatalf("warnings=%v, want offline admitted warning", result.Warnings)
	}
}

func TestGenerateRoutes_ThreeNodeRing(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", Admitted: true, Alive: true},
		{NodeID: "node-b", Admitted: true, Alive: true},
		{NodeID: "node-c", Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 1, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string][]string{
		"node-a": {"node-b"},
		"node-b": {"node-c"},
		"node-c": {"node-a"},
	}
	if got := routeMap(result); !reflect.DeepEqual(got, want) {
		t.Fatalf("routes=%v, want ring %v", got, want)
	}

	full, err := backup.GenerateRoutes(nodes, 2, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantFull := map[string][]string{
		"node-a": {"node-b", "node-c"},
		"node-b": {"node-c", "node-a"},
		"node-c": {"node-a", "node-b"},
	}
	if got := routeMap(full); !reflect.DeepEqual(got, wantFull) {
		t.Fatalf("factor=2 routes=%v, want %v", got, wantFull)
	}
}

func TestGenerateRoutes_FourNodeRing(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-d", Admitted: true, Alive: true},
		{NodeID: "node-a", Admitted: true, Alive: true},
		{NodeID: "node-c", Admitted: true, Alive: true},
		{NodeID: "node-b", Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 1, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string][]string{
		"node-a": {"node-b"},
		"node-b": {"node-c"},
		"node-c": {"node-d"},
		"node-d": {"node-a"},
	}
	if got := routeMap(result); !reflect.DeepEqual(got, want) {
		t.Fatalf("factor=1 routes=%v, want ring %v", got, want)
	}

	factorTwo, err := backup.GenerateRoutes(nodes, 2, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTwo := map[string][]string{
		"node-a": {"node-b", "node-c"},
		"node-b": {"node-c", "node-d"},
		"node-c": {"node-d", "node-a"},
		"node-d": {"node-a", "node-b"},
	}
	if got := routeMap(factorTwo); !reflect.DeepEqual(got, wantTwo) {
		t.Fatalf("factor=2 routes=%v, want ring %v", got, wantTwo)
	}
}

func TestGenerateRoutes_ZonePrefersRingAmongDifferentDomains(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", Zone: "zone-1", Admitted: true, Alive: true},
		{NodeID: "node-b", Zone: "zone-2", Admitted: true, Alive: true},
		{NodeID: "node-c", Zone: "zone-1", Admitted: true, Alive: true},
		{NodeID: "node-d", Zone: "zone-2", Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 2, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string][]string{
		"node-a": {"node-b", "node-d"},
		"node-b": {"node-c", "node-a"},
		"node-c": {"node-d", "node-b"},
		"node-d": {"node-a", "node-c"},
	}
	if got := routeMap(result); !reflect.DeepEqual(got, want) {
		t.Fatalf("routes=%v, want cross-zone ring %v", got, want)
	}
}

func TestGenerateRoutes_Deterministic(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-c", Admitted: true, Alive: true},
		{NodeID: "node-a", Admitted: true, Alive: true},
		{NodeID: "node-b", Admitted: true, Alive: true},
	}
	result1, err := backup.GenerateRoutes(nodes, 1, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	result2, err := backup.GenerateRoutes(nodes, 1, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if len(result1.Routes) != len(result2.Routes) {
		t.Fatalf("route count differs: %d vs %d", len(result1.Routes), len(result2.Routes))
	}
	for i := range result1.Routes {
		r1, r2 := result1.Routes[i], result2.Routes[i]
		if r1.SourceNodeID != r2.SourceNodeID {
			t.Fatalf("route %d source differs: %s vs %s", i, r1.SourceNodeID, r2.SourceNodeID)
		}
		if len(r1.TargetNodeIDs) != len(r2.TargetNodeIDs) {
			t.Fatalf("route %d target count differs", i)
		}
		for j := range r1.TargetNodeIDs {
			if r1.TargetNodeIDs[j] != r2.TargetNodeIDs[j] {
				t.Fatalf("route %d target %d differs: %s vs %s", i, j, r1.TargetNodeIDs[j], r2.TargetNodeIDs[j])
			}
		}
	}
}

func TestGenerateRoutes_SourceNotInTargets(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", Admitted: true, Alive: true},
		{NodeID: "node-b", Admitted: true, Alive: true},
		{NodeID: "node-c", Admitted: true, Alive: true},
		{NodeID: "node-d", Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 2, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, route := range result.Routes {
		for _, target := range route.TargetNodeIDs {
			if target == route.SourceNodeID {
				t.Fatalf("source %s appears in its own targets", route.SourceNodeID)
			}
		}
	}
}

func TestGenerateRoutes_AntiAffinityZone(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", Zone: "zone-1", Admitted: true, Alive: true},
		{NodeID: "node-b", Zone: "zone-2", Admitted: true, Alive: true},
		{NodeID: "node-c", Zone: "zone-1", Admitted: true, Alive: true},
		{NodeID: "node-d", Zone: "zone-2", Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 2, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	routeMap := make(map[string][]string)
	for _, r := range result.Routes {
		routeMap[r.SourceNodeID] = r.TargetNodeIDs
	}
	// node-a (zone-1) should prefer targets in zone-2
	nodeZones := map[string]string{"node-a": "zone-1", "node-b": "zone-2", "node-c": "zone-1", "node-d": "zone-2"}
	for source, targets := range routeMap {
		sourceZone := nodeZones[source]
		diffZoneCount := 0
		for _, target := range targets {
			if nodeZones[target] != sourceZone {
				diffZoneCount++
			}
		}
		if diffZoneCount == 0 && len(targets) > 0 {
			// Check if different zone targets were available
			availableDiffZone := 0
			for node, zone := range nodeZones {
				if node != source && zone != sourceZone {
					availableDiffZone++
				}
			}
			if availableDiffZone > 0 {
				t.Fatalf("source %s did not prefer different zone targets (available: %d)", source, availableDiffZone)
			}
		}
	}
}

func TestGenerateRoutes_NoTopologyWarning(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", Zone: "", Rack: "", Host: "", Admitted: true, Alive: true},
		{NodeID: "node-b", Zone: "", Rack: "", Host: "", Admitted: true, Alive: true},
		{NodeID: "node-c", Zone: "", Rack: "", Host: "", Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 2, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hasWarning := false
	for _, w := range result.Warnings {
		if w == "no-topology-labels" {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Fatalf("expected 'no-topology-labels' warning, got %v", result.Warnings)
	}
}

func TestGenerateRoutes_CapacityWeight(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", CapacityWeight: 1.0, Admitted: true, Alive: true},
		{NodeID: "node-b", CapacityWeight: 2.0, Admitted: true, Alive: true},
		{NodeID: "node-c", CapacityWeight: 1.0, Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 1, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Count inbound routes
	inboundCount := make(map[string]int)
	for _, route := range result.Routes {
		for _, target := range route.TargetNodeIDs {
			inboundCount[target]++
		}
	}
	// node-b with weight 2.0 should receive more inbound routes than node-a/c with weight 1.0
	if inboundCount["node-b"] <= inboundCount["node-a"] || inboundCount["node-b"] <= inboundCount["node-c"] {
		t.Fatalf("node-b (weight=2.0) should receive more routes, got inbound: %v", inboundCount)
	}
}

func TestGenerateRoutes_InsufficientCandidates(t *testing.T) {
	nodes := []backup.AgentTopology{
		{NodeID: "node-a", Admitted: true, Alive: true},
		{NodeID: "node-b", Admitted: true, Alive: true},
	}
	result, err := backup.GenerateRoutes(nodes, 3, backup.TopologyConstraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should degrade: each node can only get 1 target (the other node)
	for _, route := range result.Routes {
		if len(route.TargetNodeIDs) > 1 {
			t.Fatalf("expected at most 1 target per source, got %d for %s", len(route.TargetNodeIDs), route.SourceNodeID)
		}
	}
	// Should have warning
	hasWarning := false
	for _, w := range result.Warnings {
		if w == "insufficient-candidates-degraded" {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Fatalf("expected 'insufficient-candidates-degraded' warning, got %v", result.Warnings)
	}
}

func routeMap(result backup.RouteDraftResult) map[string][]string {
	out := make(map[string][]string, len(result.Routes))
	for _, route := range result.Routes {
		out[route.SourceNodeID] = route.TargetNodeIDs
	}
	return out
}
