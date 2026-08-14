package paths_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/paths"
)

func TestShimSocket_SanitizesColon(t *testing.T) {
	l := paths.New("/data")
	if l.ShimSocket("abc:0") != "/data/shim/abc_0.sock" {
		t.Fatal(l.ShimSocket("abc:0"))
	}
}

func TestNew_LayoutFields(t *testing.T) {
	l := paths.New("/data")
	if l.Root != "/data" {
		t.Fatalf("Root=%q", l.Root)
	}
	if l.Store != "/data/store.db" {
		t.Fatalf("Store=%q", l.Store)
	}
	if l.ShimDir != "/data/shim" {
		t.Fatalf("ShimDir=%q", l.ShimDir)
	}
	if l.LogDir != "/data/logs" {
		t.Fatalf("LogDir=%q", l.LogDir)
	}
	if l.RuntimeDir != "/data/runtime" {
		t.Fatalf("RuntimeDir=%q", l.RuntimeDir)
	}
	if l.ClusterDir != "/data/cluster" {
		t.Fatalf("ClusterDir=%q", l.ClusterDir)
	}
}

func TestEnsure_CreatesDirs0750(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "pm")
	l := paths.New(base)
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{l.Root, l.ShimDir, l.LogDir, l.RuntimeDir, l.ClusterDir} {
		st, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !st.IsDir() {
			t.Fatalf("%s is not a dir", dir)
		}
		if perm := st.Mode().Perm(); perm != 0o750 {
			t.Fatalf("%s perm=%o want 0750", dir, perm)
		}
	}
}

func TestDefaultRoot_NonEmpty(t *testing.T) {
	if paths.DefaultRoot() == "" {
		t.Fatal("empty")
	}
}

func TestDefaultRoot_NoTilde(t *testing.T) {
	if strings.Contains(paths.DefaultRoot(), "~") {
		t.Fatalf("DefaultRoot must not contain ~: %q", paths.DefaultRoot())
	}
}

func TestCurrentBootID_NonEmpty(t *testing.T) {
	if paths.CurrentBootID() == "" {
		t.Fatal("empty boot id")
	}
	if paths.CurrentBootID() != paths.CurrentBootID() {
		t.Fatal("boot id must be stable in-process")
	}
}

func TestLayout_NodeAndBootFiles(t *testing.T) {
	l := paths.New("/data")
	if l.NodeIDFile() != "/data/node_id" {
		t.Fatalf("NodeIDFile=%q", l.NodeIDFile())
	}
	if l.BootIDFile() != "/data/boot_id" {
		t.Fatalf("BootIDFile=%q", l.BootIDFile())
	}
}
