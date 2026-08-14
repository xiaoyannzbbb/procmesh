package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func newTestManager(t *testing.T) (*process.Manager, *store.Store, paths.Layout) {
	t.Helper()
	return newTestManagerNow(t, time.Now)
}

func newTestManagerNow(t *testing.T, now func() time.Time) (*process.Manager, *store.Store, paths.Layout) {
	t.Helper()
	root := shortRoot(t)
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	return process.NewManager(process.Deps{Store: st, Layout: layout, Now: now}), st, layout
}

func openStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.GetOrCreateNodeID(context.Background()); err != nil {
		t.Fatal(err)
	}
	boot, err := st.GetBootID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if boot == "" {
		if _, err := st.RotateBootID(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func shortRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newProcessClient(t *testing.T, degraded func() bool) procmeshv1connect.ProcessServiceClient {
	t.Helper()
	m, _, _ := newTestManager(t)
	return newProcessClientWith(t, m, degraded)
}

func newProcessClientWith(t *testing.T, m *process.Manager, degraded func() bool) procmeshv1connect.ProcessServiceClient {
	t.Helper()
	api := &ProcessAPI{Mgr: m, Degraded: degraded}
	mux := http.NewServeMux()
	path, h := procmeshv1connect.NewProcessServiceHandler(api)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewProcessServiceClient(srv.Client(), srv.URL)
}
