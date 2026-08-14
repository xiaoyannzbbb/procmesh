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
	proc, _ := newServiceClientsWith(t, m, nil, degraded)
	return proc
}

func newConfigClients(t *testing.T) (procmeshv1connect.ProcessServiceClient, procmeshv1connect.ConfigServiceClient) {
	t.Helper()
	m, st, _ := newTestManager(t)
	return newServiceClientsWith(t, m, st, nil)
}

func newServiceClientsWith(t *testing.T, m *process.Manager, revs RevisionStore, degraded func() bool) (procmeshv1connect.ProcessServiceClient, procmeshv1connect.ConfigServiceClient) {
	t.Helper()
	mux := http.NewServeMux()
	pp, ph := procmeshv1connect.NewProcessServiceHandler(&ProcessAPI{Mgr: m, Degraded: degraded})
	mux.Handle(pp, ph)
	cp, ch := procmeshv1connect.NewConfigServiceHandler(&ConfigAPI{Mgr: m, Revs: revs, Degraded: degraded})
	mux.Handle(cp, ch)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewProcessServiceClient(srv.Client(), srv.URL),
		procmeshv1connect.NewConfigServiceClient(srv.Client(), srv.URL)
}
