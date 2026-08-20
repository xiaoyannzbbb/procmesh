package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type rpcRuntime struct {
	mu  sync.Mutex
	srv *rpc.Server
	ln  net.Listener

	opt         Options
	dir         string
	nodeID      string
	mgr         *process.Manager
	st          *store.Store
	mesh        *cluster.Mesh
	src         *liveSource
	ready       func() error
	degraded    bool
	fwd         *agentForwarder
	auth        *auth.Service
	node        *control.Node
	raftDir     string
	clusterID   string
	controlBind string
	controlAdv  string
	knownLeader string
	started     time.Time
	logger      *slog.Logger
	metrics     *metrics.Collector
	backup      *backup.Engine
	backupCoord *backup.Coordinator
}

func (r *rpcRuntime) startRPC() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startRPCLocked()
}

func (r *rpcRuntime) rpcListening() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.srv != nil
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
	srv, err := rpc.NewServer(ln.Addr().String(), creds, clusterID, r.localHandler(), func(s string) bool {
		n := r.control()
		if n == nil {
			return false
		}
		return n.View().SerialRevoked(s)
	})
	if err != nil {
		_ = ln.Close()
		return err
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			r.logger.With("component", "rpc").Error("rpc serve failed", "error", err)
		}
	}()
	addr := advertiseRPC(r.opt.RPCAdvertise, ln.Addr().String())
	r.srv = srv
	r.ln = ln
	r.fwd.set(creds, clusterID, r.serialRevoked)
	if r.src != nil {
		r.src.setRPC(addr)
	}
	if r.mesh != nil {
		r.mesh.Update()
	}
	if r.opt.OnRPCListen != nil {
		r.opt.OnRPCListen(addr)
	}
	r.logger.With("component", "rpc").Info("rpc listening", "address", addr)
	return nil
}

func (r *rpcRuntime) serialRevoked(s string) bool {
	n := r.control()
	if n == nil {
		return false
	}
	return n.View().SerialRevoked(s)
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
	srv, ln, node := r.srv, r.ln, r.node
	r.srv, r.ln, r.node = nil, nil, nil
	r.mu.Unlock()
	if srv != nil {
		_ = srv.Shutdown(ctx)
	}
	if ln != nil {
		_ = ln.Close()
	}
	if node != nil {
		_ = node.Shutdown()
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
	ap, ah := procmeshv1connect.NewAuditServiceHandler(&api.AuditAPI{
		Store: r.st, Auth: r.auth,
		LocalOnly: true, LocalID: r.nodeID,
	}, opts...)
	mux.Handle(ap, ah)
	var localFn func() cluster.NodeSummary
	if r.src != nil {
		localFn = r.src.Snapshot
	}
	mp, mh := procmeshv1connect.NewMetricsServiceHandler(&api.MetricsAPI{
		Mgr: r.mgr, Auth: r.auth, Started: r.started,
		Cluster: api.ClusterDeps{
			Dir: r.dir, Store: r.st, Mesh: r.mesh, Local: localFn,
			ControlFn: r.control, NodeID: r.nodeID,
		},
		LocalOnly: true, LocalID: r.nodeID, Degraded: r.degradedFn(),
		Metrics: r.metrics,
		Store:   r.st,
	}, opts...)
	mux.Handle(mp, mh)
	alp, alh := procmeshv1connect.NewAlertServiceHandler(&api.AlertAPI{
		Store: r.st, Auth: r.auth,
		LocalOnly: true, LocalID: r.nodeID,
	}, opts...)
	mux.Handle(alp, alh)
	bpkup, bkh := procmeshv1connect.NewBackupServiceHandler(&api.BackupAPI{
		Engine: r.backup, Auth: r.auth,
		LocalOnly: true, LocalID: r.nodeID,
	}, opts...)
	mux.Handle(bpkup, bkh)
	cbp, cbh := procmeshv1connect.NewClusterBackupServiceHandler(&api.ClusterBackupAPI{
		Auth: r.auth, ControlFn: r.control, LocalOnly: true, LocalID: r.nodeID,
		LeaderTerm: func() uint64 {
			n := r.control()
			if n == nil {
				return 0
			}
			return n.CurrentTerm()
		},
		ApplyFn: func(cmd control.Command, timeout time.Duration) error {
			n := r.control()
			if n == nil {
				return fmt.Errorf("control plane unavailable")
			}
			return n.Apply(cmd, timeout)
		},
	}, opts...)
	mux.Handle(cbp, cbh)

	// Internal Agent-to-Agent backup task RPC (no user auth, mTLS only)
	var peerStore *backup.PeerStore
	if r.backup != nil {
		peerStore = &backup.PeerStore{Root: r.dir}
	}

	// Disaster Replication Service (control plane)
	drp, drh := procmeshv1connect.NewDisasterReplicationServiceHandler(&api.DisasterReplicationAPI{
		ClusterID: r.clusterID,
		NodeID:    r.nodeID,
		Auth:      r.auth,
		StateFn: func() control.State {
			n := r.control()
			if n == nil {
				return control.State{}
			}
			return n.View()
		},
		ApplyFn: func(cmd control.Command, timeout time.Duration) error {
			n := r.control()
			if n == nil {
				return fmt.Errorf("control plane unavailable")
			}
			return n.Apply(cmd, timeout)
		},
		PeerStore: peerStore,
		Members: func() []cluster.NodeSummary {
			if r.mesh == nil {
				return nil
			}
			return r.mesh.Members()
		},
	}, opts...)
	mux.Handle(drp, drh)

	cbap, cbah := procmeshv1connect.NewClusterBackupAgentServiceHandler(&api.ClusterBackupAgentAPI{
		Engine:    r.backup,
		Auth:      r.auth,
		ClusterID: r.clusterID,
		NodeID:    r.nodeID,
	})
	mux.Handle(cbap, cbah)

	// Internal Agent-to-Agent peer replication (no user auth, mTLS only)
	prp, prh := procmeshv1connect.NewPeerReplicationServiceHandler(&api.PeerReplicationAPI{
		PeerStore: peerStore,
		ClusterID: r.clusterID,
		NodeID:    r.nodeID,
	})
	mux.Handle(prp, prh)

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
	revoked   func(serial string) bool
}

func (f *agentForwarder) set(creds control.AgentCreds, clusterID string, revoked func(serial string) bool) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.creds = creds
	f.clusterID = clusterID
	f.revoked = revoked
	f.mu.Unlock()
}

func (f *agentForwarder) snapshot() (control.AgentCreds, string, func(string) bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.creds, f.clusterID, f.revoked
}

const (
	processHopTimeout       = rpc.MutationTimeout
	configHopTimeout        = rpc.MutationTimeout
	logHopTimeout           = time.Duration(0)
	auditHopTimeout         = 2 * time.Second
	metricsHopTimeout       = rpc.UnaryTimeout
	alertHopTimeout         = 2 * time.Second
	backupHopTimeout        = rpc.MutationTimeout
	clusterBackupHopTimeout = rpc.MutationTimeout
)

func (f *agentForwarder) dial(rt api.Route, timeout time.Duration) (*http.Client, string, error) {
	creds, clusterID, revoked := f.snapshot()
	hc, base, err := rpc.Dial(rpc.DialConfig{
		Creds: creds, ClusterID: clusterID, ExpectNodeID: rt.NodeID, Address: rt.RPC,
		Timeout: timeout, Revoked: revoked,
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

func (f *agentForwarder) Audit(_ context.Context, rt api.Route) (procmeshv1connect.AuditServiceClient, error) {
	hc, base, err := f.dial(rt, auditHopTimeout)
	if err != nil {
		return nil, err
	}
	return rpc.NewAuditClient(hc, base), nil
}

func (f *agentForwarder) Metrics(_ context.Context, rt api.Route) (procmeshv1connect.MetricsServiceClient, error) {
	hc, base, err := f.dial(rt, metricsHopTimeout)
	if err != nil {
		return nil, err
	}
	return rpc.NewMetricsClient(hc, base), nil
}

func (f *agentForwarder) Alert(_ context.Context, rt api.Route) (procmeshv1connect.AlertServiceClient, error) {
	hc, base, err := f.dial(rt, alertHopTimeout)
	if err != nil {
		return nil, err
	}
	return rpc.NewAlertClient(hc, base), nil
}

func (f *agentForwarder) Backup(_ context.Context, rt api.Route) (procmeshv1connect.BackupServiceClient, error) {
	hc, base, err := f.dial(rt, backupHopTimeout)
	if err != nil {
		return nil, err
	}
	return rpc.NewBackupClient(hc, base), nil
}

func (f *agentForwarder) ClusterBackup(_ context.Context, rt api.Route) (procmeshv1connect.ClusterBackupServiceClient, error) {
	hc, base, err := f.dial(rt, clusterBackupHopTimeout)
	if err != nil {
		return nil, err
	}
	return rpc.NewClusterBackupClient(hc, base), nil
}
