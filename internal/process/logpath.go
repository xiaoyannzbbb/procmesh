package process

import (
	"context"

	"github.com/qleelulu/procmesh/internal/logmgr"
)

func LogPathPending(latest, active LogPolicy, inst Instance) bool {
	if inst.ActiveRevision == 0 {
		return false
	}
	return latest.Directory != active.Directory || latest.RedirectStderr != active.RedirectStderr
}

func (m *Manager) EffectiveLog(ctx context.Context, spec ProcessSpec, inst Instance) LogPolicy {
	if inst.ActiveRevision <= 0 || inst.ActiveRevision == spec.LatestRevision {
		return spec.Log
	}
	old, err := m.deps.Store.GetRevisionSpec(ctx, spec.ProcessID, inst.ActiveRevision)
	if err != nil {
		return spec.Log
	}
	return old.Log
}

func (m *Manager) ReadLogPath(ctx context.Context, spec ProcessSpec, inst Instance, stream string) string {
	pol := m.EffectiveLog(ctx, spec, inst)
	stdout, stderr := logmgr.Resolve(m.deps.Layout, pol.Directory, spec.ProcessID, inst.InstanceID, inst.Ordinal)
	if stream == "stderr" {
		return stderr
	}
	return stdout
}

func (m *Manager) writeLogPaths(spec ProcessSpec, inst Instance) (stdout, stderr string) {
	stdout, stderr = logmgr.Resolve(m.deps.Layout, spec.Log.Directory, spec.ProcessID, inst.InstanceID, inst.Ordinal)
	return logmgr.WritePaths(stdout, stderr, spec.Log.RedirectStderr)
}

func (m *Manager) CustomLogDirs(ctx context.Context) []string {
	specs, err := m.deps.Store.ListSpecs(ctx)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, s := range specs {
		d := s.Log.Directory
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}
