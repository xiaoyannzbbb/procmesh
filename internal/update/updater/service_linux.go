//go:build linux

package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type SystemdService struct {
	systemctl string
}

func NewSystemdService() (*SystemdService, error) {
	for _, candidate := range []string{"/usr/bin/systemctl", "/bin/systemctl"} {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o022 == 0 {
			return &SystemdService{systemctl: candidate}, nil
		}
	}
	return nil, errors.New("trusted systemctl executable not found")
}

func (s *SystemdService) RestartAgent(ctx context.Context) error {
	if s == nil || s.systemctl == "" {
		return errors.New("systemd service is not configured")
	}
	if err := exec.CommandContext(ctx, s.systemctl, "restart", "procmesh-agent.service").Run(); err != nil {
		return fmt.Errorf("restart procmesh-agent.service: %w", err)
	}
	return nil
}

func (s *SystemdService) RunningAgentPath(ctx context.Context) (string, error) {
	if s == nil || s.systemctl == "" {
		return "", errors.New("systemd service is not configured")
	}
	output, err := exec.CommandContext(ctx, s.systemctl, "show", "--property=MainPID", "--value", "procmesh-agent.service").Output()
	if err != nil {
		return "", fmt.Errorf("read procmesh-agent.service MainPID: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || pid <= 0 {
		return "", errors.New("procmesh-agent.service has no running MainPID")
	}
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return "", fmt.Errorf("read agent executable: %w", err)
	}
	return strings.TrimSuffix(executable, " (deleted)"), nil
}
