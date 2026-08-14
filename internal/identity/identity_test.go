package identity_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/identity"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestEnsure_CreatesUUIDFileAndSyncsStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	l := paths.New(root)
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(l.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	id, err := identity.Ensure(ctx, l, st, "host-boot-1")
	if err != nil {
		t.Fatal(err)
	}
	if !isUUID(id) {
		t.Fatalf("not uuid: %q", id)
	}

	raw, err := os.ReadFile(l.NodeIDFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != id {
		t.Fatalf("file=%q store=%q", raw, id)
	}
	got, err := st.GetOrCreateNodeID(ctx)
	if err != nil || got != id {
		t.Fatalf("store=%q err=%v", got, err)
	}

	boot, err := os.ReadFile(l.BootIDFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(boot)) != "host-boot-1" {
		t.Fatalf("boot file=%q", boot)
	}
}

func TestEnsure_FileWinsOverStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	l := paths.New(root)
	_ = l.Ensure()
	st, _ := store.Open(l.Store)
	t.Cleanup(func() { _ = st.Close() })
	_, _ = st.GetOrCreateNodeID(ctx)
	const fileID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if err := os.WriteFile(l.NodeIDFile(), []byte(fileID+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	id, err := identity.Ensure(ctx, l, st, "b")
	if err != nil {
		t.Fatal(err)
	}
	if id != fileID {
		t.Fatalf("got %q", id)
	}
	got, _ := st.GetOrCreateNodeID(ctx)
	if got != fileID {
		t.Fatalf("store not synced: %q", got)
	}
}

func TestEnsure_StableAcrossCalls(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	l := paths.New(root)
	_ = l.Ensure()
	st, _ := store.Open(l.Store)
	t.Cleanup(func() { _ = st.Close() })
	a, err := identity.Ensure(ctx, l, st, "b1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.Ensure(ctx, l, st, "b2")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("%q vs %q", a, b)
	}
	boot, _ := os.ReadFile(l.BootIDFile())
	if strings.TrimSpace(string(boot)) != "b2" {
		t.Fatalf("boot file must be overwritten each Ensure: %q", boot)
	}
}

func TestEnsure_RejectsNonUUIDFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	l := paths.New(root)
	_ = l.Ensure()
	st, _ := store.Open(l.Store)
	t.Cleanup(func() { _ = st.Close() })
	_ = os.WriteFile(l.NodeIDFile(), []byte("10.0.0.1\n"), 0o640)
	_, err := identity.Ensure(ctx, l, st, "b")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID, got %v", err)
	}
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func isUUID(s string) bool {
	return uuidRE.MatchString(s)
}
