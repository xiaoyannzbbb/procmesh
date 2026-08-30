//go:build linux

package updater

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdServiceRestartsOnlyProcMeshAgent(t *testing.T) {
	directory := t.TempDir()
	argumentsFile := filepath.Join(directory, "arguments")
	systemctl := filepath.Join(directory, "systemctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + argumentsFile + "\n"
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	service := &SystemdService{systemctl: systemctl}
	if err := service.RestartAgent(context.Background()); err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(arguments)); got != "restart procmesh-agent.service" {
		t.Fatalf("systemctl arguments = %q", got)
	}
}

func TestSystemdServiceResolvesMainPIDExecutable(t *testing.T) {
	directory := t.TempDir()
	systemctl := filepath.Join(directory, "systemctl")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %d\n", os.Getpid())
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	service := &SystemdService{systemctl: systemctl}
	got, err := service.RunningAgentPath(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("RunningAgentPath() = %q, want %q", got, want)
	}
}
