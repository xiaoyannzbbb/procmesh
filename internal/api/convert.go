package api

import (
	"time"

	"github.com/qleelulu/procmesh/internal/process"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func SpecToProto(s process.ProcessSpec) *procmeshv1.ProcessSpec {
	out := &procmeshv1.ProcessSpec{
		ProcessId:        s.ProcessID,
		Name:             s.Name,
		OwnerAgentId:     s.OwnerAgentID,
		Group:            s.Group,
		Command:          s.Command,
		Args:             s.Args,
		WorkingDirectory: s.WorkingDirectory,
		RunAsUser:        s.RunAsUser,
		Environment:      s.Environment,
		Instances:        int32(s.Instances),
		Autostart:        s.Autostart,
		StopSignal:       s.StopSignal,
		KillSignal:       s.KillSignal,
		StopTimeoutMs:    durationMS(s.StopTimeout),
		StartupPriority:  int32(s.StartupPriority),
		Restart: &procmeshv1.RestartPolicy{
			Mode:          string(s.Restart.Mode),
			MaxRetries:    int32(s.Restart.MaxRetries),
			RetryWindowMs: durationMS(s.Restart.RetryWindow),
			Backoff: &procmeshv1.Backoff{
				InitialMs:  durationMS(s.Restart.Backoff.Initial),
				MaxMs:      durationMS(s.Restart.Backoff.Max),
				Multiplier: s.Restart.Backoff.Multiplier,
			},
		},
		Health: &procmeshv1.HealthCheck{
			Type:              s.Health.Type,
			Url:               s.Health.URL,
			Method:            s.Health.Method,
			Address:           s.Health.Address,
			Command:           s.Health.Command,
			ExpectedStatus:    int32(s.Health.ExpectedStatus),
			Args:              s.Health.Args,
			InitialDelayMs:    durationMS(s.Health.InitialDelay),
			IntervalMs:        durationMS(s.Health.Interval),
			TimeoutMs:         durationMS(s.Health.Timeout),
			FailureThreshold:  int32(s.Health.FailureThreshold),
			SuccessThreshold:  int32(s.Health.SuccessThreshold),
			RestartOnFailure:  s.Health.RestartOnFailure,
			RestartCooldownMs: durationMS(s.Health.RestartCooldown),
		},
		Log: &procmeshv1.LogPolicy{
			MaxSize:        s.Log.MaxSize,
			MaxFiles:       int32(s.Log.MaxFiles),
			MaxAgeSeconds:  durationSeconds(s.Log.MaxAge),
			Compress:       s.Log.Compress,
			Directory:      s.Log.Directory,
			RedirectStderr: s.Log.RedirectStderr,
		},
		Resources: &procmeshv1.ResourceLimit{
			CpuQuotaMillis: s.Resources.CPUQuotaMillis,
			MemoryBytes:    s.Resources.MemoryBytes,
			OpenFiles:      s.Resources.OpenFiles,
		},
		Dependencies:   depsToProto(s.Dependencies),
		LatestRevision: s.LatestRevision,
	}
	return out
}

func ProtoToSpec(p *procmeshv1.ProcessSpec) process.ProcessSpec {
	if p == nil {
		return process.ProcessSpec{}
	}
	out := process.ProcessSpec{
		ProcessID:        p.GetProcessId(),
		Name:             p.GetName(),
		OwnerAgentID:     p.GetOwnerAgentId(),
		Group:            p.GetGroup(),
		Command:          p.GetCommand(),
		Args:             p.GetArgs(),
		WorkingDirectory: p.GetWorkingDirectory(),
		RunAsUser:        p.GetRunAsUser(),
		Environment:      p.GetEnvironment(),
		Instances:        int(p.GetInstances()),
		Autostart:        p.GetAutostart(),
		StopSignal:       p.GetStopSignal(),
		KillSignal:       p.GetKillSignal(),
		StopTimeout:      fromMS(p.GetStopTimeoutMs()),
		StartupPriority:  int(p.GetStartupPriority()),
		LatestRevision:   p.GetLatestRevision(),
	}
	if r := p.GetRestart(); r != nil {
		out.Restart = process.RestartPolicy{
			Mode:        process.RestartMode(r.GetMode()),
			MaxRetries:  int(r.GetMaxRetries()),
			RetryWindow: fromMS(r.GetRetryWindowMs()),
		}
		if b := r.GetBackoff(); b != nil {
			out.Restart.Backoff = process.Backoff{
				Initial:    fromMS(b.GetInitialMs()),
				Max:        fromMS(b.GetMaxMs()),
				Multiplier: b.GetMultiplier(),
			}
		}
	}
	if h := p.GetHealth(); h != nil {
		out.Health = process.HealthCheckSpec{
			Type:             h.GetType(),
			URL:              h.GetUrl(),
			Method:           h.GetMethod(),
			Address:          h.GetAddress(),
			Command:          h.GetCommand(),
			ExpectedStatus:   int(h.GetExpectedStatus()),
			Args:             h.GetArgs(),
			InitialDelay:     fromMS(h.GetInitialDelayMs()),
			Interval:         fromMS(h.GetIntervalMs()),
			Timeout:          fromMS(h.GetTimeoutMs()),
			FailureThreshold: int(h.GetFailureThreshold()),
			SuccessThreshold: int(h.GetSuccessThreshold()),
			RestartOnFailure: h.GetRestartOnFailure(),
			RestartCooldown:  fromMS(h.GetRestartCooldownMs()),
		}
	}
	if l := p.GetLog(); l != nil {
		out.Log = process.LogPolicy{
			MaxSize:        l.GetMaxSize(),
			MaxFiles:       int(l.GetMaxFiles()),
			MaxAge:         fromSeconds(l.GetMaxAgeSeconds()),
			Compress:       l.GetCompress(),
			Directory:      l.GetDirectory(),
			RedirectStderr: l.GetRedirectStderr(),
		}
	}
	if r := p.GetResources(); r != nil {
		out.Resources = process.ResourceLimit{
			CPUQuotaMillis: r.GetCpuQuotaMillis(),
			MemoryBytes:    r.GetMemoryBytes(),
			OpenFiles:      r.GetOpenFiles(),
		}
	}
	out.Dependencies = depsFromProto(p.GetDependencies())
	return out
}

func ViewOf(spec process.ProcessSpec, insts []process.Instance) *procmeshv1.ProcessView {
	view := &procmeshv1.ProcessView{
		ProcessId: spec.ProcessID,
		Spec:      SpecToProto(spec),
	}
	for _, inst := range insts {
		pi := &procmeshv1.Instance{
			InstanceId:     inst.InstanceID,
			Ordinal:        int32(inst.Ordinal),
			Desired:        string(inst.Desired),
			Observed:       string(inst.Observed),
			Health:         string(inst.Health),
			Pid:            int32(inst.PID),
			ActiveRevision: inst.ActiveRevision,
			RestartCount:   int32(inst.RestartCount),
			LastError:      inst.LastError,
		}
		if inst.ExitCode != nil {
			pi.ExitCode = int32(*inst.ExitCode)
			pi.HasExitCode = true
		}
		if inst.StartedAt != nil {
			pi.StartedUnixMs = inst.StartedAt.UTC().UnixMilli()
		}
		view.Instances = append(view.Instances, pi)
	}
	return view
}

func depsToProto(in []process.Dependency) []*procmeshv1.Dependency {
	if len(in) == 0 {
		return nil
	}
	out := make([]*procmeshv1.Dependency, len(in))
	for i, d := range in {
		out[i] = &procmeshv1.Dependency{ProcessName: d.ProcessName, Condition: string(d.Condition)}
	}
	return out
}

func depsFromProto(in []*procmeshv1.Dependency) []process.Dependency {
	if len(in) == 0 {
		return nil
	}
	out := make([]process.Dependency, 0, len(in))
	for _, d := range in {
		if d == nil {
			continue
		}
		out = append(out, process.Dependency{
			ProcessName: d.GetProcessName(),
			Condition:   process.DepCondition(d.GetCondition()),
		})
	}
	return out
}

func durationMS(d time.Duration) int64 { return d.Milliseconds() }

func fromMS(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

func durationSeconds(d time.Duration) int64 { return int64(d / time.Second) }

func fromSeconds(sec int64) time.Duration { return time.Duration(sec) * time.Second }
