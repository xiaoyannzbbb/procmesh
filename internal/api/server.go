package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/localhttp"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/process"
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
	Addr      string
	Mgr       *process.Manager
	Logs      *logmgr.Manager
	Store     RevisionStore // 可为 *store.Store；nil 时 Config.Diff 不可用
	Cluster   ClusterDeps   // 零值 = 未接线；Init/Join → UNAVAILABLE
	Degraded  bool
	Ready     func() error
	Started   time.Time
	LocalOnly bool
	LocalID   string
	Router    *Router
	Forward   Forwarder
}

func NewServer(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.Started.IsZero() {
		opts.Started = time.Now()
	}

	rpcForwardTotal := &atomic.Uint64{}
	opts.Forward = wrapForwarder(opts.Forward, rpcForwardTotal)

	engine := gin.New()
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
	pp, ph := procmeshv1connect.NewProcessServiceHandler(&ProcessAPI{
		Mgr: opts.Mgr, Degraded: degraded,
		LocalOnly: opts.LocalOnly, LocalID: opts.LocalID, Router: opts.Router, Forward: opts.Forward,
	})
	mountConnect(engine, pp, ph)
	cp, ch := procmeshv1connect.NewConfigServiceHandler(&ConfigAPI{
		Mgr: opts.Mgr, Revs: opts.Store, Degraded: degraded,
		LocalOnly: opts.LocalOnly, LocalID: opts.LocalID, Router: opts.Router, Forward: opts.Forward,
	})
	mountConnect(engine, cp, ch)
	lp, lh := procmeshv1connect.NewLogServiceHandler(&LogAPI{
		Mgr:       opts.Mgr,
		LocalOnly: opts.LocalOnly, LocalID: opts.LocalID, Router: opts.Router, Forward: opts.Forward,
	})
	mountConnect(engine, lp, lh)
	np, nh := procmeshv1connect.NewNodeServiceHandler(&NodeAPI{Deps: opts.Cluster, Degraded: degraded})
	mountConnect(engine, np, nh)
	clp, clh := procmeshv1connect.NewClusterServiceHandler(&ClusterAPI{Deps: opts.Cluster, Degraded: degraded})
	mountConnect(engine, clp, clh)

	legacy, err := localhttp.NewServerOpts(opts.Mgr, opts.Logs, opts.Addr, opts.Degraded, opts.Ready)
	if err != nil {
		return nil, err
	}
	engine.Any("/v1/*path", gin.WrapH(wrapLegacyV1(legacy.Handler, s.blockLegacyMutations)))

	return s, nil
}

const legacyMutationMsg = "use connect rpc for remote mutations"

func wrapLegacyV1(next http.Handler, block func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if block() && isLegacyProcessMutation(r) {
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
	))
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
