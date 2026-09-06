package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/identity"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestRun_InvalidLogFormatExitsTwo(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--data-dir", t.TempDir(), "--log-format", "yaml"}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "log format") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRun_InvalidLogLevelExitsTwo(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--data-dir", t.TempDir(), "--log-level", "trace"}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "log level") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRun_HelpExitsZero(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--help"}, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "log-format") || !strings.Contains(stderr.String(), "log-level") || !strings.Contains(stderr.String(), "break-glass-socket") || !strings.Contains(stderr.String(), "break-glass-group") || !strings.Contains(stderr.String(), "pprof-listen") || !strings.Contains(stderr.String(), "advertise") || !strings.Contains(stderr.String(), "reset-node-identity") {
		t.Fatalf("help missing logging flags: %q", stderr.String())
	}
}

func TestRun_ResetNodeIdentityRequiresExplicitDataDir(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--reset-node-identity"}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "requires --data-dir") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRun_ResetNodeIdentityAndExit(t *testing.T) {
	root := t.TempDir()
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Ensure(context.Background(), layout, st, "test-boot"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	clusterDir := layout.ClusterDir
	if err := os.WriteFile(filepath.Join(clusterDir, "join.pending.json"), []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(kept, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	code := run([]string{"--data-dir", root, "--reset-node-identity"}, &stderr)
	if code != 0 || !strings.Contains(stderr.String(), "node identity reset; node_id=") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(clusterDir, "join.pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending join remains: %v", err)
	}
	if raw, err := os.ReadFile(kept); err != nil || string(raw) != "keep" {
		t.Fatalf("preserved file=%q err=%v", raw, err)
	}
}
