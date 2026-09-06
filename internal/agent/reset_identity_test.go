package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/agent"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/identity"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestResetNodeIdentity_RotatesUnjoinedIdentityAndPreservesLocalData(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	layout := paths.New(root)
	oldID := prepareLocalIdentity(t, ctx, layout)
	pending := filepath.Join(layout.ClusterDir, "join.pending.json")
	if err := os.WriteFile(pending, []byte("pending-private-identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	temporaryPending := filepath.Join(layout.ClusterDir, ".join.pending-crash")
	if err := os.WriteFile(temporaryPending, []byte("temporary-private-identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(layout.LogDir, "keep.log")
	if err := os.WriteFile(kept, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}

	newID, err := agent.ResetNodeIdentity(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if newID == "" || newID == oldID {
		t.Fatalf("new node id=%q old=%q", newID, oldID)
	}
	raw, err := os.ReadFile(layout.NodeIDFile())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != newID {
		t.Fatalf("node_id file=%q want %q", got, newID)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if got, err := st.GetOrCreateNodeID(ctx); err != nil || got != newID {
		t.Fatalf("store node_id=%q err=%v want %q", got, err, newID)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending join remains: %v", err)
	}
	if _, err := os.Stat(temporaryPending); !os.IsNotExist(err) {
		t.Fatalf("temporary pending join remains: %v", err)
	}
	if raw, err := os.ReadFile(kept); err != nil || string(raw) != "keep" {
		t.Fatalf("preserved log=%q err=%v", raw, err)
	}
}

func TestResetNodeIdentity_RequiresExistingAbsoluteIdentity(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	empty := t.TempDir()
	for _, dataDir := range []string{"relative/data", missing, empty} {
		t.Run(strings.ReplaceAll(dataDir, string(filepath.Separator), "_"), func(t *testing.T) {
			_, err := agent.ResetNodeIdentity(context.Background(), dataDir)
			if !errcode.Is(err, errcode.INVALID) {
				t.Fatalf("want INVALID, got %v", err)
			}
		})
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing path was created: %v", err)
	}
}

func TestResetNodeIdentity_RefusesInitializedClusterWithoutChangingFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	layout := paths.New(root)
	oldID := prepareLocalIdentity(t, ctx, layout)
	pending := filepath.Join(layout.ClusterDir, "join.pending.json")
	if err := os.WriteFile(pending, []byte("pending-private-identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.ClusterDir, "cluster.json"), []byte("{}\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	_, err := agent.ResetNodeIdentity(ctx, root)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT, got %v", err)
	}
	raw, readErr := os.ReadFile(layout.NodeIDFile())
	if readErr != nil || strings.TrimSpace(string(raw)) != oldID {
		t.Fatalf("node_id changed: raw=%q err=%v", raw, readErr)
	}
	if raw, readErr := os.ReadFile(pending); readErr != nil || string(raw) != "pending-private-identity" {
		t.Fatalf("pending join changed: raw=%q err=%v", raw, readErr)
	}
}

func TestResetNodeIdentity_RefusesPersistedClusterOrRaftState(t *testing.T) {
	for _, relativePath := range []string{
		filepath.Join("cluster", "agent.crt"),
		filepath.Join("raft", "raft.db"),
	} {
		t.Run(relativePath, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			layout := paths.New(root)
			oldID := prepareLocalIdentity(t, ctx, layout)
			statePath := filepath.Join(root, relativePath)
			if err := os.WriteFile(statePath, []byte("persisted-state"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := agent.ResetNodeIdentity(ctx, root)
			if !errcode.Is(err, errcode.CONFLICT) {
				t.Fatalf("want CONFLICT, got %v", err)
			}
			raw, readErr := os.ReadFile(layout.NodeIDFile())
			if readErr != nil || strings.TrimSpace(string(raw)) != oldID {
				t.Fatalf("node_id changed: raw=%q err=%v", raw, readErr)
			}
			if raw, readErr := os.ReadFile(statePath); readErr != nil || string(raw) != "persisted-state" {
				t.Fatalf("persisted state changed: raw=%q err=%v", raw, readErr)
			}
		})
	}
}

func TestResetNodeIdentity_RefusesRunningAgent(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx, agent.Options{
			DataDir:       root,
			Listen:        "127.0.0.1:0",
			GossipListen:  "127.0.0.1:0",
			RPCListen:     "127.0.0.1:0",
			ControlListen: "127.0.0.1:0",
			OnListen:      func(string) { ready <- struct{}{} },
		})
	}()
	select {
	case <-ready:
	case err := <-errCh:
		t.Fatalf("Agent exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Agent")
	}

	_, err := agent.ResetNodeIdentity(context.Background(), root)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT, got %v", err)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Agent did not stop")
	}
}

func prepareLocalIdentity(t *testing.T, ctx context.Context, layout paths.Layout) string {
	t.Helper()
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.Ensure(ctx, layout, st, "test-boot")
	if closeErr := st.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return id
}
