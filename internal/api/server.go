package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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

	opts Options
}

type Options struct {
	Addr     string
	Mgr      *process.Manager
	Logs     *logmgr.Manager
	Store    RevisionStore // 可为 *store.Store；nil 时 Config.Diff 不可用
	Cluster  ClusterDeps   // 零值 = 未接线；Init/Join → UNAVAILABLE
	Degraded bool
	Ready    func() error
	Started  time.Time
}

func NewServer(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.Started.IsZero() {
		opts.Started = time.Now()
	}

	engine := gin.New()
	s := &Server{
		Engine: engine,
		HTTP:   &http.Server{Addr: opts.Addr, Handler: engine},
		opts:   opts,
	}

	engine.GET("/healthz", s.healthz)
	engine.GET("/readyz", s.readyz)
	engine.GET("/metrics", s.metrics)

	degraded := s.isDegraded
	pp, ph := procmeshv1connect.NewProcessServiceHandler(&ProcessAPI{Mgr: opts.Mgr, Degraded: degraded})
	mountConnect(engine, pp, ph)
	cp, ch := procmeshv1connect.NewConfigServiceHandler(&ConfigAPI{Mgr: opts.Mgr, Revs: opts.Store, Degraded: degraded})
	mountConnect(engine, cp, ch)
	lp, lh := procmeshv1connect.NewLogServiceHandler(&LogAPI{Mgr: opts.Mgr})
	mountConnect(engine, lp, lh)
	np, nh := procmeshv1connect.NewNodeServiceHandler(&NodeAPI{Deps: opts.Cluster, Degraded: degraded})
	mountConnect(engine, np, nh)
	clp, clh := procmeshv1connect.NewClusterServiceHandler(&ClusterAPI{Deps: opts.Cluster, Degraded: degraded})
	mountConnect(engine, clp, clh)

	legacy, err := localhttp.NewServerOpts(opts.Mgr, opts.Logs, opts.Addr, opts.Degraded, opts.Ready)
	if err != nil {
		return nil, err
	}
	engine.Any("/v1/*path", gin.WrapH(legacy.Handler))

	return s, nil
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
	c.Data(http.StatusOK, prometheusContentType, renderMetrics(time.Since(s.opts.Started).Seconds(), runningInstances(s.opts.Mgr)))
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
