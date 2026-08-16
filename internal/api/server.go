package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/gin-gonic/gin"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/localhttp"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/web"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

type Server struct {
	Engine *gin.Engine
	HTTP   *http.Server

	opts            Options
	rpcForwardTotal *atomic.Uint64
}

type Options struct {
	Addr          string
	Logger        *slog.Logger
	Mgr           *process.Manager
	Logs          *logmgr.Manager
	Store         RevisionStore // 可为 *store.Store；nil 时 Config.Diff 不可用
	Cluster       ClusterDeps   // 零值 = 未接线；Init/Join → UNAVAILABLE
	Auth          *auth.Service // nil = 不鉴权（单测）
	Degraded      bool
	Ready         func() error
	Started       time.Time
	LocalOnly     bool
	LocalID       string
	Router        *Router
	Forward       Forwarder
	HasQuorum     func() bool
	RPCHealthy    func() bool
	GossipHealthy func() bool
	CertExpires   func() int64
	CAExpires     func() int64
	Members       func() []cluster.NodeSummary
	Metrics       *metrics.Collector
}

func NewServer(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.Started.IsZero() {
		opts.Started = time.Now()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Cluster.RPCHealthy == nil {
		opts.Cluster.RPCHealthy = opts.RPCHealthy
	}
	if opts.Cluster.GossipHealthy == nil {
		opts.Cluster.GossipHealthy = opts.GossipHealthy
	}
	if opts.Cluster.CertExpires == nil {
		opts.Cluster.CertExpires = opts.CertExpires
	}
	if opts.Cluster.CAExpires == nil {
		opts.Cluster.CAExpires = opts.CAExpires
	}

	rpcForwardTotal := &atomic.Uint64{}
	opts.Forward = wrapForwarder(opts.Forward, rpcForwardTotal)

	engine := gin.New()
	engine.Use(accessLog(opts.Logger))
	s := &Server{
		Engine:          engine,
		HTTP:            &http.Server{Addr: opts.Addr, Handler: engine},
		opts:            opts,
		rpcForwardTotal: rpcForwardTotal,
	}

	engine.GET("/healthz", s.healthz)
	engine.GET("/readyz", s.readyz)
	engine.GET("/metrics", s.metrics)

	degraded := s.isDegraded
	intercept := connect.WithInterceptors(AuthInterceptor(opts.Auth, s.clusterInited))
	pp, ph := procmeshv1connect.NewProcessServiceHandler(&ProcessAPI{
		Mgr: opts.Mgr, Auth: opts.Auth, Degraded: degraded,
		LocalOnly: opts.LocalOnly, LocalID: opts.LocalID, Router: opts.Router, Forward: opts.Forward,
	}, intercept)
	mountConnect(engine, pp, ph)
	cp, ch := procmeshv1connect.NewConfigServiceHandler(&ConfigAPI{
		Mgr: opts.Mgr, Auth: opts.Auth, Revs: opts.Store, Degraded: degraded,
		LocalOnly: opts.LocalOnly, LocalID: opts.LocalID, Router: opts.Router, Forward: opts.Forward,
	}, intercept)
	mountConnect(engine, cp, ch)
	lp, lh := procmeshv1connect.NewLogServiceHandler(&LogAPI{
		Mgr: opts.Mgr, Auth: opts.Auth,
		LocalOnly: opts.LocalOnly, LocalID: opts.LocalID, Router: opts.Router, Forward: opts.Forward,
	}, intercept)
	mountConnect(engine, lp, lh)
	np, nh := procmeshv1connect.NewNodeServiceHandler(&NodeAPI{Deps: opts.Cluster, Auth: opts.Auth, Degraded: degraded}, intercept)
	mountConnect(engine, np, nh)
	clp, clh := procmeshv1connect.NewClusterServiceHandler(&ClusterAPI{
		Deps: opts.Cluster, Auth: opts.Auth, Degraded: degraded,
		Logger: opts.Logger.With("component", "cluster"),
	}, intercept)
	mountConnect(engine, clp, clh)
	ap, ah := procmeshv1connect.NewAuthServiceHandler(&AuthAPI{Auth: opts.Auth}, intercept)
	mountConnect(engine, ap, ah)
	up, uh := procmeshv1connect.NewUserServiceHandler(&UserAPI{Auth: opts.Auth}, intercept)
	mountConnect(engine, up, uh)
	rp, rh := procmeshv1connect.NewRoleServiceHandler(&RoleAPI{Auth: opts.Auth}, intercept)
	mountConnect(engine, rp, rh)
	gp, gh := procmeshv1connect.NewGroupServiceHandler(&GroupAPI{Auth: opts.Auth}, intercept)
	mountConnect(engine, gp, gh)
	adp, adh := procmeshv1connect.NewAuditServiceHandler(newAuditAPI(opts), intercept)
	mountConnect(engine, adp, adh)
	mp, mh := procmeshv1connect.NewMetricsServiceHandler(&MetricsAPI{
		Mgr: opts.Mgr, Auth: opts.Auth, Started: opts.Started, Cluster: opts.Cluster,
		LocalOnly: opts.LocalOnly, LocalID: opts.LocalID, Router: opts.Router, Forward: opts.Forward,
		Degraded: degraded, Metrics: opts.Metrics,
	}, intercept)
	mountConnect(engine, mp, mh)

	legacy, err := localhttp.NewServerOpts(opts.Mgr, opts.Logs, opts.Addr, opts.Degraded, opts.Ready)
	if err != nil {
		return nil, err
	}
	engine.Any("/v1/*path", gin.WrapH(wrapLegacyV1(legacy.Handler, s.clusterInited, s.blockLegacyMutations)))

	engine.NoRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Status(http.StatusNotFound)
			return
		}
		web.Handler().ServeHTTP(c.Writer, c.Request)
	})

	return s, nil
}

func accessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		logger.Debug("http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(started).Milliseconds(),
			"remote_addr", c.Request.RemoteAddr,
		)
	}
}

const (
	legacyMutationMsg = "use connect rpc for remote mutations"
	legacyDeniedMsg   = "use connect rpc"
)

func wrapLegacyV1(next http.Handler, inited, blockMutations func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inited != nil && inited() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(localhttp.APIError{
				Code:    string(errcode.DENIED),
				Message: legacyDeniedMsg,
			})
			return
		}
		if blockMutations != nil && blockMutations() && isLegacyProcessMutation(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(localhttp.APIError{
				Code:    string(errcode.UNAVAILABLE),
				Message: legacyMutationMsg,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLegacyProcessMutation(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return strings.HasPrefix(r.URL.Path, "/v1/processes") || strings.HasPrefix(r.URL.Path, "/v1/instances")
	default:
		return false
	}
}

func (s *Server) blockLegacyMutations() bool {
	if s.clusterInited() {
		return true
	}
	// NewServer tests wire Router without Cluster deps. Agent serveHTTP
	// always sets Router, so Router alone is not enough there.
	return s.opts.Router != nil && s.opts.Cluster.Store == nil && s.opts.Cluster.Dir == ""
}

func (s *Server) clusterInited() bool {
	if id := s.opts.Cluster.clusterID(context.Background()); id != "" {
		return true
	}
	if s.opts.Cluster.Dir != "" {
		if _, err := control.LoadAgentCreds(s.opts.Cluster.Dir); err == nil {
			return true
		}
	}
	return false
}

func (s *Server) Serve(l net.Listener) error {
	return s.HTTP.Serve(l)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.HTTP.Shutdown(ctx)
}

func (s *Server) healthz(c *gin.Context) {
	c.String(http.StatusOK, "ok")
}

func (s *Server) readyz(c *gin.Context) {
	if s.isDegraded() {
		c.String(http.StatusServiceUnavailable, "DEGRADED")
		return
	}
	c.String(http.StatusOK, "ok")
}

func (s *Server) metrics(c *gin.Context) {
	members, alive := clusterMemberCounts(s.opts.Cluster)
	var rpcForward uint64
	if s.rpcForwardTotal != nil {
		rpcForward = s.rpcForwardTotal.Load()
	}
	c.Data(http.StatusOK, prometheusContentType, renderMetrics(
		time.Since(s.opts.Started).Seconds(),
		runningInstances(s.opts.Mgr),
		members,
		alive,
		rpcForward,
		s.controlQuorum(),
	))
}

func (s *Server) controlQuorum() int {
	if s.opts.HasQuorum != nil {
		if s.opts.HasQuorum() {
			return 1
		}
		return 0
	}
	if n := s.opts.Cluster.controlNode(); n != nil && n.HasQuorum() {
		return 1
	}
	if s.opts.Auth != nil {
		if st := s.opts.Auth.Store(); st != nil && st.HasQuorum() {
			return 1
		}
	}
	return 0
}

func clusterMemberCounts(d ClusterDeps) (members, alive int) {
	ms := d.members()
	members = len(ms)
	for _, n := range ms {
		if n.State == cluster.StateAlive {
			alive++
		}
	}
	return members, alive
}

func (s *Server) isDegraded() bool {
	if s.opts.Degraded || s.opts.Mgr == nil {
		return true
	}
	if s.opts.Ready != nil && s.opts.Ready() != nil {
		return true
	}
	return false
}

func mountConnect(engine *gin.Engine, path string, h http.Handler) {
	engine.Any(strings.TrimSuffix(path, "/")+"/*path", gin.WrapH(h))
}

func newAuditAPI(opts Options) *AuditAPI {
	var st *store.Store
	if s, ok := opts.Store.(*store.Store); ok {
		st = s
	}
	members := opts.Members
	if members == nil && opts.Router != nil {
		members = opts.Router.Members
	}
	if members == nil {
		members = opts.Cluster.members
	}
	return &AuditAPI{
		Store:     st,
		Auth:      opts.Auth,
		LocalOnly: opts.LocalOnly,
		LocalID:   opts.LocalID,
		Router:    opts.Router,
		Forward:   opts.Forward,
		Members:   members,
	}
}
