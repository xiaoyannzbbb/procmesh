package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/version"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
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
	proc, cfg, _, _ := newServiceClientsFull(t, m, revs, degraded)
	return proc, cfg
}

func newLogClients(t *testing.T) (procmeshv1connect.ProcessServiceClient, procmeshv1connect.LogServiceClient, *process.Manager, paths.Layout) {
	t.Helper()
	proc, logs, m, layout, _ := newLogClientsAPI(t)
	return proc, logs, m, layout
}

func newLogClientsAPI(t *testing.T) (procmeshv1connect.ProcessServiceClient, procmeshv1connect.LogServiceClient, *process.Manager, paths.Layout, *LogAPI) {
	t.Helper()
	m, _, layout := newTestManager(t)
	proc, _, logs, api := newServiceClientsFull(t, m, nil, nil)
	return proc, logs, m, layout, api
}

func newServiceClientsFull(t *testing.T, m *process.Manager, revs RevisionStore, degraded func() bool) (
	procmeshv1connect.ProcessServiceClient,
	procmeshv1connect.ConfigServiceClient,
	procmeshv1connect.LogServiceClient,
	*LogAPI,
) {
	t.Helper()
	logAPI := &LogAPI{Mgr: m}
	mux := http.NewServeMux()
	pp, ph := procmeshv1connect.NewProcessServiceHandler(&ProcessAPI{Mgr: m, Degraded: degraded})
	mux.Handle(pp, ph)
	cp, ch := procmeshv1connect.NewConfigServiceHandler(&ConfigAPI{Mgr: m, Revs: revs, Degraded: degraded})
	mux.Handle(cp, ch)
	lp, lh := procmeshv1connect.NewLogServiceHandler(logAPI)
	mux.Handle(lp, lh)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewProcessServiceClient(srv.Client(), srv.URL),
		procmeshv1connect.NewConfigServiceClient(srv.Client(), srv.URL),
		procmeshv1connect.NewLogServiceClient(srv.Client(), srv.URL),
		logAPI
}

func remoteOwnerRouter(localID, ownerID, processName string) *Router {
	return &Router{
		LocalID: localID,
		Members: func() []cluster.NodeSummary {
			var procs []cluster.ProcessSummary
			if processName != "" {
				procs = []cluster.ProcessSummary{{Name: processName}}
			}
			return []cluster.NodeSummary{{
				NodeID: ownerID, Hostname: "host-" + ownerID, State: cluster.StateAlive,
				RPCAddress: "127.0.0.1:9003", ProtocolVersion: version.Protocol,
				Processes: procs,
			}}
		},
		LocalHasName: func(context.Context, string) bool { return false },
	}
}

func serveProcessAPI(t *testing.T, api *ProcessAPI, interceptors ...connect.Interceptor) procmeshv1connect.ProcessServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	pp, ph := procmeshv1connect.NewProcessServiceHandler(api, handlerOpts(interceptors)...)
	mux.Handle(pp, ph)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewProcessServiceClient(srv.Client(), srv.URL)
}

func serveConfigAPI(t *testing.T, api *ConfigAPI, interceptors ...connect.Interceptor) procmeshv1connect.ConfigServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	cp, ch := procmeshv1connect.NewConfigServiceHandler(api, handlerOpts(interceptors)...)
	mux.Handle(cp, ch)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewConfigServiceClient(srv.Client(), srv.URL)
}

func serveLogAPI(t *testing.T, api *LogAPI, interceptors ...connect.Interceptor) procmeshv1connect.LogServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	lp, lh := procmeshv1connect.NewLogServiceHandler(api, handlerOpts(interceptors)...)
	mux.Handle(lp, lh)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return procmeshv1connect.NewLogServiceClient(srv.Client(), srv.URL)
}

func handlerOpts(interceptors []connect.Interceptor) []connect.HandlerOption {
	if len(interceptors) == 0 {
		return nil
	}
	return []connect.HandlerOption{connect.WithInterceptors(interceptors...)}
}

type fakeForwarder struct {
	mu       sync.Mutex
	processN int
	configN  int
	logN     int
	auditN   int
	metricsN int
	alertN   int
	routes   []Route
	proc     procmeshv1connect.ProcessServiceClient
	cfg      procmeshv1connect.ConfigServiceClient
	logs     procmeshv1connect.LogServiceClient
	audit    procmeshv1connect.AuditServiceClient
	metrics  procmeshv1connect.MetricsServiceClient
	alert    procmeshv1connect.AlertServiceClient
	err      error
}

func (f *fakeForwarder) Process(_ context.Context, rt Route) (procmeshv1connect.ProcessServiceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.processN++
	f.routes = append(f.routes, rt)
	if f.err != nil {
		return nil, f.err
	}
	return f.proc, nil
}

func (f *fakeForwarder) Config(_ context.Context, rt Route) (procmeshv1connect.ConfigServiceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configN++
	f.routes = append(f.routes, rt)
	if f.err != nil {
		return nil, f.err
	}
	return f.cfg, nil
}

func (f *fakeForwarder) Log(_ context.Context, rt Route) (procmeshv1connect.LogServiceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logN++
	f.routes = append(f.routes, rt)
	if f.err != nil {
		return nil, f.err
	}
	return f.logs, nil
}

func (f *fakeForwarder) Audit(_ context.Context, rt Route) (procmeshv1connect.AuditServiceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auditN++
	f.routes = append(f.routes, rt)
	if f.err != nil {
		return nil, f.err
	}
	return f.audit, nil
}

func (f *fakeForwarder) Metrics(_ context.Context, rt Route) (procmeshv1connect.MetricsServiceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metricsN++
	f.routes = append(f.routes, rt)
	if f.err != nil {
		return nil, f.err
	}
	return f.metrics, nil
}

func (f *fakeForwarder) Alert(_ context.Context, rt Route) (procmeshv1connect.AlertServiceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alertN++
	f.routes = append(f.routes, rt)
	if f.err != nil {
		return nil, f.err
	}
	return f.alert, nil
}

func (f *fakeForwarder) processCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.processN
}

func (f *fakeForwarder) configCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.configN
}

func (f *fakeForwarder) logCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logN
}

func (f *fakeForwarder) metricsCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.metricsN
}

type fakeProcessClient struct {
	mu          sync.Mutex
	restarts    []*connect.Request[procmeshv1.ProcessRefRequest]
	applies     []*connect.Request[procmeshv1.ApplyProcessRequest]
	restartResp *connect.Response[procmeshv1.ProcessRefResponse]
	applyResp   *connect.Response[procmeshv1.ApplyProcessResponse]
	err         error
}

func (f *fakeProcessClient) ListProcesses(context.Context, *connect.Request[procmeshv1.ListProcessesRequest]) (*connect.Response[procmeshv1.ListProcessesResponse], error) {
	return connect.NewResponse(&procmeshv1.ListProcessesResponse{}), nil
}

func (f *fakeProcessClient) GetProcess(context.Context, *connect.Request[procmeshv1.GetProcessRequest]) (*connect.Response[procmeshv1.GetProcessResponse], error) {
	return connect.NewResponse(&procmeshv1.GetProcessResponse{}), nil
}

func (f *fakeProcessClient) ApplyProcess(_ context.Context, req *connect.Request[procmeshv1.ApplyProcessRequest]) (*connect.Response[procmeshv1.ApplyProcessResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies = append(f.applies, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.applyResp != nil {
		return f.applyResp, nil
	}
	return connect.NewResponse(&procmeshv1.ApplyProcessResponse{Spec: req.Msg.GetSpec()}), nil
}

func (f *fakeProcessClient) DeleteProcess(context.Context, *connect.Request[procmeshv1.DeleteProcessRequest]) (*connect.Response[procmeshv1.DeleteProcessResponse], error) {
	return connect.NewResponse(&procmeshv1.DeleteProcessResponse{}), nil
}

func (f *fakeProcessClient) StartProcess(context.Context, *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return connect.NewResponse(&procmeshv1.ProcessRefResponse{}), nil
}

func (f *fakeProcessClient) StopProcess(context.Context, *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return connect.NewResponse(&procmeshv1.ProcessRefResponse{}), nil
}

func (f *fakeProcessClient) RestartProcess(_ context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts = append(f.restarts, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.restartResp != nil {
		return f.restartResp, nil
	}
	return connect.NewResponse(&procmeshv1.ProcessRefResponse{
		Process: &procmeshv1.ProcessView{Spec: &procmeshv1.ProcessSpec{Name: req.Msg.GetIdOrName()}},
	}), nil
}

func (f *fakeProcessClient) KillProcess(context.Context, *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return connect.NewResponse(&procmeshv1.ProcessRefResponse{}), nil
}

func (f *fakeProcessClient) ResetFailure(context.Context, *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	return connect.NewResponse(&procmeshv1.ProcessRefResponse{}), nil
}

func (f *fakeProcessClient) AdoptInstance(context.Context, *connect.Request[procmeshv1.AdoptRequest]) (*connect.Response[procmeshv1.AdoptResponse], error) {
	return connect.NewResponse(&procmeshv1.AdoptResponse{}), nil
}

func (f *fakeProcessClient) restartReqs() []*connect.Request[procmeshv1.ProcessRefRequest] {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*connect.Request[procmeshv1.ProcessRefRequest], len(f.restarts))
	copy(out, f.restarts)
	return out
}

func (f *fakeProcessClient) applyReqs() []*connect.Request[procmeshv1.ApplyProcessRequest] {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*connect.Request[procmeshv1.ApplyProcessRequest], len(f.applies))
	copy(out, f.applies)
	return out
}

type fakeConfigClient struct {
	mu           sync.Mutex
	updates      []*connect.Request[procmeshv1.UpdateConfigRequest]
	rollbacks    []*connect.Request[procmeshv1.RollbackRequest]
	updateResp   *connect.Response[procmeshv1.UpdateConfigResponse]
	rollbackResp *connect.Response[procmeshv1.RollbackResponse]
	err          error
}

func (f *fakeConfigClient) GetConfig(context.Context, *connect.Request[procmeshv1.GetConfigRequest]) (*connect.Response[procmeshv1.GetConfigResponse], error) {
	return connect.NewResponse(&procmeshv1.GetConfigResponse{}), nil
}

func (f *fakeConfigClient) UpdateConfig(_ context.Context, req *connect.Request[procmeshv1.UpdateConfigRequest]) (*connect.Response[procmeshv1.UpdateConfigResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.updateResp != nil {
		return f.updateResp, nil
	}
	return connect.NewResponse(&procmeshv1.UpdateConfigResponse{Spec: req.Msg.GetSpec()}), nil
}

func (f *fakeConfigClient) History(context.Context, *connect.Request[procmeshv1.HistoryRequest]) (*connect.Response[procmeshv1.HistoryResponse], error) {
	return connect.NewResponse(&procmeshv1.HistoryResponse{}), nil
}

func (f *fakeConfigClient) Diff(context.Context, *connect.Request[procmeshv1.DiffRequest]) (*connect.Response[procmeshv1.DiffResponse], error) {
	return connect.NewResponse(&procmeshv1.DiffResponse{}), nil
}

func (f *fakeConfigClient) Rollback(_ context.Context, req *connect.Request[procmeshv1.RollbackRequest]) (*connect.Response[procmeshv1.RollbackResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbacks = append(f.rollbacks, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.rollbackResp != nil {
		return f.rollbackResp, nil
	}
	return connect.NewResponse(&procmeshv1.RollbackResponse{
		Spec: &procmeshv1.ProcessSpec{Name: req.Msg.GetIdOrName()},
	}), nil
}

func (f *fakeConfigClient) updateReqs() []*connect.Request[procmeshv1.UpdateConfigRequest] {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*connect.Request[procmeshv1.UpdateConfigRequest], len(f.updates))
	copy(out, f.updates)
	return out
}

func (f *fakeConfigClient) rollbackReqs() []*connect.Request[procmeshv1.RollbackRequest] {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*connect.Request[procmeshv1.RollbackRequest], len(f.rollbacks))
	copy(out, f.rollbacks)
	return out
}

type fakeLogClient struct {
	mu      sync.Mutex
	tails   []*connect.Request[procmeshv1.TailLogsRequest]
	tailOut *connect.Response[procmeshv1.TailLogsResponse]
	err     error
}

func (f *fakeLogClient) TailLogs(_ context.Context, req *connect.Request[procmeshv1.TailLogsRequest]) (*connect.Response[procmeshv1.TailLogsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tails = append(f.tails, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.tailOut != nil {
		return f.tailOut, nil
	}
	return connect.NewResponse(&procmeshv1.TailLogsResponse{Lines: []string{"remote"}}), nil
}

func (f *fakeLogClient) StreamLogs(context.Context, *connect.Request[procmeshv1.StreamLogsRequest]) (*connect.ServerStreamForClient[procmeshv1.LogChunk], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unimplemented"))
}

func (f *fakeLogClient) DownloadLogs(context.Context, *connect.Request[procmeshv1.DownloadLogsRequest]) (*connect.ServerStreamForClient[procmeshv1.LogChunk], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unimplemented"))
}

func (f *fakeLogClient) tailReqs() []*connect.Request[procmeshv1.TailLogsRequest] {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*connect.Request[procmeshv1.TailLogsRequest], len(f.tails))
	copy(out, f.tails)
	return out
}
