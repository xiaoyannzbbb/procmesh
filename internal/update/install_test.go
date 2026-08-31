package update_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/update"
)

func TestTarInstaller_PathTraversal(t *testing.T) {
	body := makeTarGz(t, map[string][]byte{
		"../procmesh-agent": []byte("evil-agent"),
		"procmesh-shim":     []byte("new-shim"),
		"procmesh":          []byte("new-cli"),
	})
	dest := t.TempDir()
	prev := filepath.Join(t.TempDir(), "previous")
	writeFile(t, filepath.Join(dest, "procmesh-agent"), "old-agent")

	err := update.TarInstaller{}.Install(context.Background(), body, dest, prev)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err=%v", err)
	}
	if got := readFile(t, filepath.Join(dest, "procmesh-agent")); got != "old-agent" {
		t.Fatalf("dest mutated: %q", got)
	}
}

func TestTarInstaller_AbsolutePath(t *testing.T) {
	body := makeTarGz(t, map[string][]byte{
		"/tmp/procmesh-agent": []byte("evil-agent"),
		"procmesh-shim":       []byte("new-shim"),
		"procmesh":            []byte("new-cli"),
	})
	dest := t.TempDir()
	err := update.TarInstaller{}.Install(context.Background(), body, dest, filepath.Join(t.TempDir(), "previous"))
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err=%v", err)
	}
}

func TestTarInstaller_MissingBinary(t *testing.T) {
	body := makeTarGz(t, map[string][]byte{
		"procmesh-agent": []byte("new-agent"),
		"procmesh":       []byte("new-cli"),
	})
	dest := t.TempDir()
	err := update.TarInstaller{}.Install(context.Background(), body, dest, filepath.Join(t.TempDir(), "previous"))
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err=%v", err)
	}
}

func TestTarInstaller_ReplaceAndKeepOnePrevious(t *testing.T) {
	dest := t.TempDir()
	prev := filepath.Join(t.TempDir(), "update", "previous")
	if err := os.MkdirAll(prev, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dest, "procmesh-agent"), "old-agent")
	writeFile(t, filepath.Join(dest, "procmesh-shim"), "old-shim")
	writeFile(t, filepath.Join(dest, "procmesh"), "old-cli")
	writeFile(t, filepath.Join(prev, "procmesh-agent"), "stale-agent")
	writeFile(t, filepath.Join(prev, "procmesh-shim"), "stale-shim")
	writeFile(t, filepath.Join(prev, "procmesh"), "stale-cli")

	body := makeTarGz(t, releaseFiles("0.2.0", map[string][]byte{
		"procmesh-agent": []byte("new-agent"),
		"procmesh-shim":  []byte("new-shim"),
		"procmesh":       []byte("new-cli"),
	}))
	inst := update.TarInstaller{}
	if err := inst.Install(context.Background(), body, dest, prev); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, filepath.Join(dest, "procmesh-agent")); got != "new-agent" {
		t.Fatalf("agent=%q", got)
	}
	if got := readFile(t, filepath.Join(dest, "procmesh-shim")); got != "new-shim" {
		t.Fatalf("shim=%q", got)
	}
	if got := readFile(t, filepath.Join(dest, "procmesh")); got != "new-cli" {
		t.Fatalf("cli=%q", got)
	}
	if got := readFile(t, filepath.Join(prev, "procmesh-agent")); got != "old-agent" {
		t.Fatalf("previous agent=%q (want replaced old, not stale)", got)
	}
	if got := readFile(t, filepath.Join(prev, "procmesh-shim")); got != "old-shim" {
		t.Fatalf("previous shim=%q", got)
	}
	if got := readFile(t, filepath.Join(prev, "procmesh")); got != "old-cli" {
		t.Fatalf("previous cli=%q", got)
	}

	entries, err := os.ReadDir(filepath.Dir(prev))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "previous" {
		t.Fatalf("expected a single previous bundle, got %v", names(entries))
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func validReleaseTarball(t *testing.T) []byte {
	t.Helper()
	return makeTarGz(t, releaseFiles("0.2.0", map[string][]byte{
		"procmesh-agent": []byte("new-agent"),
		"procmesh-shim":  []byte("new-shim"),
		"procmesh":       []byte("new-cli"),
	}))
}

func errorChainHasPath(err error, path string) bool {
	for err != nil {
		if path != "" && strings.Contains(err.Error(), path) {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func TestTarInstaller_StatFailureHasNoPath(t *testing.T) {
	body := validReleaseTarball(t)
	dest := filepath.Join(t.TempDir(), "not-a-dir")
	writeFile(t, dest, "not-a-directory")
	prev := filepath.Join(t.TempDir(), "previous")

	err := update.TarInstaller{}.Install(context.Background(), body, dest, prev)
	if err == nil {
		t.Fatal("expected stat failure")
	}
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), dest) {
		t.Fatalf("path leaked in Error: %v", err)
	}
	if errorChainHasPath(err, dest) {
		t.Fatalf("path leaked in unwrap chain: %v", err)
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		t.Fatalf("PathError leaked via unwrap: %v", pe)
	}
}

func TestTarInstaller_RenameFailureHasNoPath(t *testing.T) {
	body := validReleaseTarball(t)
	dest := t.TempDir()
	prev := filepath.Join(t.TempDir(), "previous")
	agentDir := filepath.Join(dest, "procmesh-agent")
	if err := os.Mkdir(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := update.TarInstaller{}.Install(context.Background(), body, dest, prev)
	if err == nil {
		t.Fatal("expected rename failure")
	}
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), dest) || strings.Contains(err.Error(), agentDir) {
		t.Fatalf("path leaked in Error: %v", err)
	}
	if errorChainHasPath(err, dest) || errorChainHasPath(err, agentDir) {
		t.Fatalf("path leaked in unwrap chain: %v", err)
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		t.Fatalf("PathError leaked via unwrap: %v", pe)
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		t.Fatalf("LinkError leaked via unwrap: %v", le)
	}
}
