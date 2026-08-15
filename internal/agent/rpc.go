package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type rpcRuntime struct {
	mu  sync.Mutex
	srv *rpc.Server
	ln  net.Listener

	opt      Options
	dir      string
	nodeID   string
	mgr      *process.Manager
	st       *store.Store
	mesh     *cluster.Mesh
	src      *liveSource
	ready    func() error
	degraded bool
	fwd      *agentForwarder
	auth     *auth.Service
	node     *control.Node
}

func (r *rpcRuntime) startRPC() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startRPCLocked()
}

func (r *rpcRuntime) startRPCLocked() error {
	if r.srv != nil {
		return nil
	}
	if r.dir == "" || !agentCertExists(r.dir) {
		return nil
	}
	creds, err := control.LoadAgentCreds(r.dir)
	if err != nil {
		return err
	}
	clusterID := r.lookupClusterID(creds)
	if clusterID == "" {
		return fmt.Errorf("cluster id required for rpc")
	}
	listen := r.opt.RPCListen
	if listen == "" {
		listen = defaultRPCListen
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("rpc listen: %w", err)
	}
	node := r.node
	srv, err := rpc.NewServer(ln.Addr().String(), creds, clusterID, r.localHandler(), func(s string) bool {
		return node.View().SerialRevoked(s)
	})
	if err != nil {
		_ = ln.Close()
		return err
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "rpc serve: %v\n", err)
		}
	}()
	addr := advertiseRPC(r.opt.RPCAdvertise, ln.Addr().String())
	r.srv = srv
	r.ln = ln
	r.fwd.set(creds, clusterID)
	if r.src != nil {
		r.src.setRPC(addr)
	}
	if r.mesh != nil {
		r.mesh.Update()
	}
	if r.opt.OnRPCListen != nil {
		r.opt.OnRPCListen(addr)
	}
	return nil
}

func (r *rpcRuntime) lookupClusterID(creds control.AgentCreds) string {
	if r.st != nil {
		if id, err := r.st.GetClusterID(context.Background()); err == nil && id != "" {
			return id
		}
	}
	if cid, _, err := control.ParseIDs(creds.AgentCertPEM); err == nil && cid != "" {
		return cid
	}
	if r.dir != "" {
		if meta, err := control.LoadMeta(r.dir); err == nil {
			return meta.ClusterID
		}
	}
	return ""
}

func (r *rpcRuntime) shutdown(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	srv, ln := r.srv, r.ln
	r.srv, r.ln = nil, nil
	r.mu.Unlock()
	if srv != nil {
		_ = srv.Shutdown(ctx)
	}
	if ln != nil {
		_ = ln.Close()
	}
}

func (r *rpcRuntime) localHandler() http.Handler {
	mux := http.NewServeMux()
	degraded := r.degradedFn()
	var revs api.RevisionStore
	if r.st != nil {
		revs = r.st
	}
	var opts []connect.HandlerOption
	if r.auth != nil {
		opts = append(opts, connect.WithInterceptors(api.OwnerAuthInterceptor(r.auth, r.nodeID)))
	}
	pp, ph := procmeshv1connect.NewProcessServiceHandler(&api.ProcessAPI{
		Mgr: r.mgr, Auth: r.auth, Degraded: degraded,
		LocalOnly: true, LocalID: r.nodeID,
	}, opts...)
	mux.Handle(pp, ph)
	cp, ch := procmeshv1connect.NewConfigServiceHandler(&api.ConfigAPI{
		Mgr: r.mgr, Auth: r.auth, Revs: revs, Degraded: degraded,
		LocalOnly: true, LocalID: r.nodeID,
	}, opts...)
	mux.Handle(cp, ch)
	lp, lh := procmeshv1connect.NewLogServiceHandler(&api.LogAPI{
		Mgr: r.mgr, Auth: r.auth,
		LocalOnly: true, LocalID: r.nodeID,
	}, opts...)
	mux.Handle(lp, lh)
	return mux
}

func (r *rpcRuntime) degradedFn() func() bool {
	return func() bool {
		if r.degraded || r.mgr == nil {
			return true
		}
		if r.ready != nil && r.ready() != nil {
			return true
		}
		return false
	}
}

func advertiseRPC(advertise, bound string) string {
	if advertise == "" {
		return bound
	}
	host, portStr, err := net.SplitHostPort(advertise)
	if err != nil {
		return advertise
	}
	if portStr != "0" {
		return advertise
	}
	_, boundPort, err := net.SplitHostPort(bound)
	if err != nil {
		return advertise
	}
	return net.JoinHostPort(host, boundPort)
}

func localHasNameFn(mgr *process.Manager) func(context.Context, string) bool {
	return func(ctx context.Context, idOrName string) bool {
		if mgr == nil || idOrName == "" {
			return false
		}
		_, err := mgr.Resolve(ctx, idOrName)
		return err == nil
	}
}

type agentForwarder struct {
	mu        sync.RWMutex
	creds     control.AgentCreds
	clusterID string
}

func (f *agentForwarder) set(creds control.AgentCreds, clusterID string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.creds = creds
	f.clusterID = clusterID
	f.mu.Unlock()
}

func (f *agentForwarder) snapshot() (control.AgentCreds, string) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.creds, f.clusterID
}

const (
	processHopTimeout = rpc.MutationTimeout
	configHopTimeout  = rpc.MutationTimeout
	logHopTimeout     = time.Duration(0)
)

func (f *agentForwarder) dial(rt api.Route, timeout time.Duration) (*http.Client, string, error) {
	creds, clusterID := f.snapshot()
	hc, base, err := rpc.Dial(rpc.DialConfig{
		Creds: creds, ClusterID: clusterID, ExpectNodeID: rt.NodeID, Address: rt.RPC,
		Timeout: timeout,
	})
	if err != nil {
		return nil, "", rpc.MapDialError(err)
	}
	return hc, base, nil
}

func (f *agentForwarder) Process(_ context.Context, rt api.Route) (procmeshv1connect.ProcessServiceClient, error) {
	hc, base, err := f.dial(rt, processHopTimeout)
	if err != nil {
		return nil, err
	}
	return rpc.NewProcessClient(hc, base), nil
}

func (f *agentForwarder) Config(_ context.Context, rt api.Route) (procmeshv1connect.ConfigServiceClient, error) {
	hc, base, err := f.dial(rt, configHopTimeout)
	if err != nil {
		return nil, err
	}
	return rpc.NewConfigClient(hc, base), nil
}

func (f *agentForwarder) Log(_ context.Context, rt api.Route) (procmeshv1connect.LogServiceClient, error) {
	hc, base, err := f.dial(rt, logHopTimeout)
	if err != nil {
		return nil, err
	}
	return rpc.NewLogClient(hc, base), nil
}
