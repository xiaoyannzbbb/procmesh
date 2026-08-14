package process

import (
	"sort"

	"github.com/qleelulu/procmesh/internal/errcode"
)

type DepCondition string

const (
	DepStarted DepCondition = "STARTED"
	DepHealthy DepCondition = "HEALTHY"
)

// StartupOrder returns process IDs in Kahn topological order.
// Among ready nodes, sort by StartupPriority ascending then Name ascending.
// Cycle → INVALID "circular dependency". Missing dep name → INVALID.
func StartupOrder(specs []ProcessSpec) ([]string, error) {
	byName := make(map[string]ProcessSpec, len(specs))
	indegree := make(map[string]int, len(specs))
	for _, s := range specs {
		byName[s.Name] = s
		indegree[s.Name] = 0
	}

	dependents := make(map[string][]string, len(specs))
	for _, s := range specs {
		seen := make(map[string]struct{}, len(s.Dependencies))
		for _, d := range s.Dependencies {
			_ = effectiveCondition(d.Condition)
			if _, ok := byName[d.ProcessName]; !ok {
				return nil, errcode.E(errcode.INVALID, "missing dependency")
			}
			if _, ok := seen[d.ProcessName]; ok {
				continue
			}
			seen[d.ProcessName] = struct{}{}
			dependents[d.ProcessName] = append(dependents[d.ProcessName], s.Name)
			indegree[s.Name]++
		}
	}

	ready := make([]ProcessSpec, 0, len(specs))
	for _, s := range specs {
		if indegree[s.Name] == 0 {
			ready = append(ready, s)
		}
	}

	order := make([]string, 0, len(specs))
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool {
			if ready[i].StartupPriority != ready[j].StartupPriority {
				return ready[i].StartupPriority < ready[j].StartupPriority
			}
			return ready[i].Name < ready[j].Name
		})
		next := ready[0]
		ready = ready[1:]
		order = append(order, next.ProcessID)
		for _, name := range dependents[next.Name] {
			indegree[name]--
			if indegree[name] == 0 {
				ready = append(ready, byName[name])
			}
		}
	}
	if len(order) != len(specs) {
		return nil, errcode.E(errcode.INVALID, "circular dependency")
	}
	return order, nil
}

func effectiveCondition(c DepCondition) DepCondition {
	if c == "" {
		return DepHealthy
	}
	return c
}

// RejectCycle reports INVALID "circular dependency" if the specs form a
// cycle among names that are present. Edges to unknown names are ignored
// so specs can be saved before their dependencies exist.
func RejectCycle(specs []ProcessSpec) error {
	names := make(map[string]struct{}, len(specs))
	for _, s := range specs {
		names[s.Name] = struct{}{}
	}
	filtered := make([]ProcessSpec, len(specs))
	for i, s := range specs {
		filtered[i] = s
		if len(s.Dependencies) == 0 {
			continue
		}
		deps := make([]Dependency, 0, len(s.Dependencies))
		for _, d := range s.Dependencies {
			if _, ok := names[d.ProcessName]; ok {
				deps = append(deps, d)
			}
		}
		filtered[i].Dependencies = deps
	}
	_, err := StartupOrder(filtered)
	return err
}

// DepsReady reports whether every declared dependency currently satisfies
// its condition. byName is keyed by spec Name, not process ID.
func DepsReady(spec ProcessSpec, byName map[string][]Instance) bool {
	for _, d := range spec.Dependencies {
		insts := byName[d.ProcessName]
		if len(insts) == 0 {
			return false
		}
		cond := d.Condition
		if cond == "" {
			cond = DepHealthy
		}
		for _, inst := range insts {
			switch cond {
			case DepStarted:
				if inst.Observed != ObservedRunning {
					return false
				}
			default:
				if inst.Health != HealthHealthy {
					return false
				}
			}
		}
	}
	return true
}
