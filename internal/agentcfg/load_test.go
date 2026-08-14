package agentcfg_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/agentcfg"
	"github.com/qleelulu/procmesh/internal/logmgr"
)

func TestLoad_MissingOptionalUsesDefaults(t *testing.T) {
	p, err := agentcfg.Load(filepath.Join(t.TempDir(), "nope.yaml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if p != logmgr.DefaultPolicy() {
		t.Fatalf("%+v", p)
	}
}

func TestLoad_MissingRequiredErrors(t *testing.T) {
	_, err := agentcfg.Load(filepath.Join(t.TempDir(), "nope.yaml"), true)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_PartialDiskKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte("disk:\n  auto_delete: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := agentcfg.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !p.AutoDelete || p.WarnPercent != 85 || !p.EmergencyStopWrites {
		t.Fatalf("%+v", p)
	}
}

func TestLoad_InvalidOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	body := "disk:\n  warn_percent: 90\n  cleanup_percent: 80\n  emergency_percent: 95\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Load(path, true); err == nil {
		t.Fatal("expected validate error")
	}
}

func TestDefaultPath_DarwinOrLinux(t *testing.T) {
	p := agentcfg.DefaultPath()
	if runtime.GOOS == "darwin" {
		if !strings.HasSuffix(p, "/.config/procmesh/agent.yaml") {
			t.Fatalf("%s", p)
		}
	} else if p != "/etc/procmesh/agent.yaml" {
		t.Fatalf("%s", p)
	}
}
