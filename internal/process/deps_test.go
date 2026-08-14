package process_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
)

func TestStartupOrder_PriorityAndCycle(t *testing.T) {
	specs := []process.ProcessSpec{
		{ProcessID: "api", Name: "api", StartupPriority: 30, Dependencies: []process.Dependency{{ProcessName: "mysql", Condition: process.DepHealthy}}},
		{ProcessID: "mysql", Name: "mysql", StartupPriority: 10},
		{ProcessID: "redis", Name: "redis", StartupPriority: 20},
	}
	got, err := process.StartupOrder(specs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mysql", "redis", "api"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	cycle := []process.ProcessSpec{
		{ProcessID: "a", Name: "a", Dependencies: []process.Dependency{{ProcessName: "b"}}},
		{ProcessID: "b", Name: "b", Dependencies: []process.Dependency{{ProcessName: "a"}}},
	}
	if _, err := process.StartupOrder(cycle); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}

func TestStartupOrder_MissingDependency(t *testing.T) {
	specs := []process.ProcessSpec{
		{ProcessID: "api", Name: "api", Dependencies: []process.Dependency{{ProcessName: "mysql"}}},
	}
	_, err := process.StartupOrder(specs)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}

func TestStartupOrder_CycleMessage(t *testing.T) {
	cycle := []process.ProcessSpec{
		{ProcessID: "a", Name: "a", Dependencies: []process.Dependency{{ProcessName: "b"}}},
		{ProcessID: "b", Name: "b", Dependencies: []process.Dependency{{ProcessName: "a"}}},
	}
	_, err := process.StartupOrder(cycle)
	if err == nil || !strings.Contains(err.Error(), "circular dependency") {
		t.Fatalf("want circular dependency got %v", err)
	}
}

func TestStartupOrder_SamePrioritySortsByName(t *testing.T) {
	specs := []process.ProcessSpec{
		{ProcessID: "id-z", Name: "zeta", StartupPriority: 5},
		{ProcessID: "id-a", Name: "alpha", StartupPriority: 5},
		{ProcessID: "id-m", Name: "mu", StartupPriority: 5},
	}
	got, err := process.StartupOrder(specs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"id-a", "id-m", "id-z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestStartupOrder_ReturnsProcessIDs(t *testing.T) {
	specs := []process.ProcessSpec{
		{ProcessID: "pid-web", Name: "web", StartupPriority: 20, Dependencies: []process.Dependency{{ProcessName: "db"}}},
		{ProcessID: "pid-db", Name: "db", StartupPriority: 10},
	}
	got, err := process.StartupOrder(specs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pid-db", "pid-web"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestDepsReady_EmptyDepsTrue(t *testing.T) {
	if !process.DepsReady(process.ProcessSpec{Name: "solo"}, nil) {
		t.Fatal("no dependencies should be ready")
	}
}

func TestDepsReady_MissingInstancesNotReady(t *testing.T) {
	spec := process.ProcessSpec{
		Name:         "api",
		Dependencies: []process.Dependency{{ProcessName: "mysql", Condition: process.DepStarted}},
	}
	if process.DepsReady(spec, map[string][]process.Instance{}) {
		t.Fatal("missing dep instances must not be ready")
	}
	if process.DepsReady(spec, map[string][]process.Instance{"mysql": {}}) {
		t.Fatal("empty dep instance list must not be ready")
	}
}

func TestDepsReady_EmptyConditionIsHealthy(t *testing.T) {
	spec := process.ProcessSpec{
		Name:         "api",
		Dependencies: []process.Dependency{{ProcessName: "mysql"}},
	}
	running := []process.Instance{{Observed: process.ObservedRunning, Health: process.HealthUnknown}}
	if process.DepsReady(spec, map[string][]process.Instance{"mysql": running}) {
		t.Fatal("empty condition is HEALTHY; UNKNOWN is not ready")
	}
	healthy := []process.Instance{{Observed: process.ObservedRunning, Health: process.HealthHealthy}}
	if !process.DepsReady(spec, map[string][]process.Instance{"mysql": healthy}) {
		t.Fatal("HEALTHY dep should be ready")
	}
}

func TestDepsReady_StartedRequiresObservedRunning(t *testing.T) {
	spec := process.ProcessSpec{
		Name:         "api",
		Dependencies: []process.Dependency{{ProcessName: "mysql", Condition: process.DepStarted}},
	}
	stopped := []process.Instance{{Observed: process.ObservedStopped}}
	if process.DepsReady(spec, map[string][]process.Instance{"mysql": stopped}) {
		t.Fatal("STOPPED is not STARTED")
	}
	running := []process.Instance{{Observed: process.ObservedRunning, Health: process.HealthUnknown}}
	if !process.DepsReady(spec, map[string][]process.Instance{"mysql": running}) {
		t.Fatal("RUNNING satisfies STARTED even if health is UNKNOWN")
	}
}

func TestDepsReady_AllInstancesMustSatisfy(t *testing.T) {
	spec := process.ProcessSpec{
		Name:         "api",
		Dependencies: []process.Dependency{{ProcessName: "mysql", Condition: process.DepHealthy}},
	}
	mixed := []process.Instance{
		{Observed: process.ObservedRunning, Health: process.HealthHealthy},
		{Observed: process.ObservedRunning, Health: process.HealthUnknown},
	}
	if process.DepsReady(spec, map[string][]process.Instance{"mysql": mixed}) {
		t.Fatal("every dep instance must be HEALTHY")
	}
}

func TestDepsReady_KeyedBySpecName(t *testing.T) {
	spec := process.ProcessSpec{
		Name:         "api",
		Dependencies: []process.Dependency{{ProcessName: "mysql", Condition: process.DepStarted}},
	}
	// Process ID must not be used as the lookup key.
	byID := map[string][]process.Instance{
		"pid-mysql": {{Observed: process.ObservedRunning}},
	}
	if process.DepsReady(spec, byID) {
		t.Fatal("byName must be keyed by spec Name, not process ID")
	}
	byName := map[string][]process.Instance{
		"mysql": {{Observed: process.ObservedRunning}},
	}
	if !process.DepsReady(spec, byName) {
		t.Fatal("lookup by spec Name should succeed")
	}
}

func TestStartupOrder_EmptyConditionAccepted(t *testing.T) {
	specs := []process.ProcessSpec{
		{ProcessID: "api", Name: "api", StartupPriority: 20, Dependencies: []process.Dependency{{ProcessName: "mysql"}}},
		{ProcessID: "mysql", Name: "mysql", StartupPriority: 10},
	}
	got, err := process.StartupOrder(specs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mysql", "api"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
