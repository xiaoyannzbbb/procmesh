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
