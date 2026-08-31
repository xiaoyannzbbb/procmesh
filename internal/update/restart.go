package update

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// AgentRestarter restarts procmesh-agent via systemd when the unit is
// available, otherwise self-execs the replaced binary with the same argv.
// KillMode is not modified (the packaged unit uses KillMode=process).
type AgentRestarter struct {
	UnitAvailable func() bool
	RunSystemctl  func(ctx context.Context, args ...string) error
	AgentPath     string
	Args          []string
	Env           []string
	Exec          func(argv0 string, argv, envv []string) error
}

func (r AgentRestarter) Restart(ctx context.Context) error {
	if r.unitAvailable() {
		run := r.RunSystemctl
		if run == nil {
			run = defaultRunSystemctl
		}
		if err := run(ctx, "restart", "procmesh-agent"); err != nil {
			return errcode.E(errcode.UNAVAILABLE, "systemctl restart failed")
		}
		return nil
	}

	path := strings.TrimSpace(r.AgentPath)
	if path == "" {
		return errcode.E(errcode.UNAVAILABLE, "no restart method available")
	}
	execFn := r.Exec
	if execFn == nil {
		execFn = defaultSelfExec
	}
	args := r.Args
	if len(args) == 0 {
		args = os.Args
	}
	env := r.Env
	if env == nil {
		env = os.Environ()
	}
	if err := execFn(path, args, env); err != nil {
		if errcode.Is(err, errcode.UNAVAILABLE) {
			return err
		}
		return errcode.E(errcode.UNAVAILABLE, "self-exec failed")
	}
	return nil
}

func (r AgentRestarter) unitAvailable() bool {
	if r.UnitAvailable != nil {
		return r.UnitAvailable()
	}
	return defaultSystemdUnitAvailable()
}

func defaultRunSystemctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	return cmd.Run()
}

func defaultSystemdUnitAvailable() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	cmd := exec.Command("systemctl", "cat", "procmesh-agent.service")
	return cmd.Run() == nil
}
