package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/qleelulu/procmesh/internal/agentcfg"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/identity"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/version"
)

const defaultGossipListen = "127.0.0.1:7946"
const defaultRPCListen = "127.0.0.1:9001"

// Options is the procmesh-agent runtime configuration.
type Options struct {
	DataDir         string
	Listen          string
	ShimBin         string
	InsecureListen  bool
	OnListen        func(addr string)
	ConfigPath      string
	GossipListen    string // default 127.0.0.1:7946
	GossipAdvertise string
	RPCListen       string // default 127.0.0.1:9001; tests use 127.0.0.1:0
	RPCAdvertise    string
	OnRPCListen     func(addr string)
	BootID          string // empty = paths.CurrentBootID(); tests may override
}

// Run owns the agent lifecycle and blocks until ctx is cancelled.
func Run(ctx context.Context, opt Options) error {
	if opt.DataDir == "" {
		return fmt.Errorf("data-dir required")
	}
	if opt.Listen == "" {
		opt.Listen = "127.0.0.1:9000"
	}
	if err := CheckListen(opt.Listen, opt.InsecureListen); err != nil {
		return err
	}

	layout := paths.New(opt.DataDir)
	if err := layout.Ensure(); err != nil {
		return fmt.Errorf("ensure layout: %w", err)
	}

	degraded := false
	st, err := store.Open(layout.Store)
	if err != nil {
		quarantine := layout.Store + ".corrupt-" + strconv.FormatInt(time.Now().Unix(), 10)
		if rerr := os.Rename(layout.Store, quarantine); rerr != nil {
			fmt.Fprintf(os.Stderr, "quarantine store: %v (open: %v)\n", rerr, err)
		} else {
			fmt.Fprintf(os.Stderr, "quarantined corrupt store to %s: %v\n", quarantine, err)
		}
		st, err = store.Open(layout.Store)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reopen store after quarantine: %v\n", err)
			return serveHTTP(ctx, opt, nil, nil, nil, true, func() error {
				return errcode.E(errcode.DEGRADED, "store unavailable")
			}, nil, nil, api.ClusterDeps{})
		}
		degraded = true
	}
	defer func() { _ = st.Close() }()

	if err := st.IntegrityCheck(ctx); err != nil {
		degraded = true
		fmt.Fprintf(os.Stderr, "integrity check: %v\n", err)
	}

	hostBoot := opt.BootID
	if hostBoot == "" {
		hostBoot = paths.CurrentBootID()
	}
	if err := st.SetBootID(ctx, hostBoot); err != nil {
		return fmt.Errorf("set boot id: %w", err)
	}
	if _, err := identity.Ensure(ctx, layout, st, hostBoot); err != nil {
		return fmt.Errorf("ensure identity: %w", err)
	}

	path := opt.ConfigPath
	required := path != ""
	if path == "" {
		path = agentcfg.DefaultPath()
	}
	cfg, err := agentcfg.LoadAll(path, required)
	if err != nil {
		return err
	}
	logs := &logmgr.Manager{Root: layout.Root, Now: time.Now, Policy: cfg.Disk}
	mgr := process.NewManager(process.Deps{
		Store:    st,
		Layout:   layout,
		ShimBin:  opt.ShimBin,
		Now:      time.Now,
		LookUser: lookupUser,
		Logs:     logs,
	})
	if err := mgr.Recover(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "recover failed: %v\n", err)
	}
	if err := mgr.Reconcile(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
	}

	gossipListen := opt.GossipListen
	if gossipListen == "" {
		gossipListen = cfg.Gossip.Listen
	}
	if gossipListen == "" {
		gossipListen = defaultGossipListen
	}
	gossipAdvertise := opt.GossipAdvertise
	if gossipAdvertise == "" {
		gossipAdvertise = cfg.Gossip.Advertise
	}
	if err := CheckListen(gossipListen, opt.InsecureListen); err != nil {
		return err
	}
	if opt.RPCListen == "" {
		opt.RPCListen = cfg.RPC.Listen
	}
	if opt.RPCListen == "" {
		opt.RPCListen = defaultRPCListen
	}
	if opt.RPCAdvertise == "" {
		opt.RPCAdvertise = cfg.RPC.Advertise
	}
	if err := CheckListen(opt.RPCListen, opt.InsecureListen); err != nil {
		return err
	}
	bindAddr, bindPort, err := splitListen(gossipListen)
	if err != nil {
		return err
	}

	nodeID, err := st.GetOrCreateNodeID(ctx)
	if err != nil {
		return fmt.Errorf("node id: %w", err)
	}
	hostname, _ := os.Hostname()
	src := &liveSource{
		nodeID:   nodeID,
		hostname: hostname,
		bootID:   hostBoot,
		store:    st,
		mgr:      mgr,
	}

	if control.AlreadyInited(layout.ClusterDir) || agentCertExists(layout.ClusterDir) {
		// Joiners persist agent.crt without ca.key; skip LoadBundle unless the seed CA key is present.
		if _, err := os.Stat(filepath.Join(layout.ClusterDir, "ca.key")); err == nil {
			if _, err := control.LoadBundle(layout.ClusterDir); err != nil {
				fmt.Fprintf(os.Stderr, "load cluster bundle: %v\n", err)
			}
		}
	}

	mesh, err := cluster.Start(cluster.Config{
		NodeID:    nodeID,
		BindAddr:  bindAddr,
		BindPort:  bindPort,
		Advertise: gossipAdvertise,
		Source:    src,
		Protocol:  version.Protocol,
		TestFast:  bindPort == 0,
	})
	if err != nil {
		return fmt.Errorf("start mesh: %w", err)
	}
	src.setGossip(mesh.LocalAddr())
	if meta, err := control.LoadMeta(layout.ClusterDir); err == nil && len(meta.GossipSeeds) > 0 {
		if _, err := mesh.Join(meta.GossipSeeds); err != nil {
			fmt.Fprintf(os.Stderr, "rejoin gossip: %v\n", err)
		}
	}

	ready := func() error {
		if err := st.IntegrityCheck(context.Background()); err != nil {
			return err
		}
		b, err := os.ReadFile(layout.Store)
		if err != nil {
			return errcode.E(errcode.DEGRADED, err.Error())
		}
		if len(b) < 16 || string(b[:15]) != "SQLite format 3" {
			return errcode.E(errcode.DEGRADED, "store file corrupted")
		}
		return nil
	}
	return serveHTTP(ctx, opt, mgr, logs, st, degraded, ready, mesh, src, api.ClusterDeps{
		Dir:        layout.ClusterDir,
		Store:      st,
		Mesh:       mesh,
		Local:      src.Snapshot,
		GossipAddr: mesh.LocalAddr,
		NodeID:     nodeID,
		Hostname:   hostname,
		BootID:     hostBoot,
	})
}

func serveHTTP(ctx context.Context, opt Options, mgr *process.Manager, logs *logmgr.Manager, st *store.Store, degraded bool, ready func() error, mesh *cluster.Mesh, src *liveSource, clusterDeps api.ClusterDeps) error {
	ln, err := net.Listen("tcp", opt.Listen)
	if err != nil {
		shutdownMesh(mesh)
		return fmt.Errorf("listen: %w", err)
	}
	apiAddr := ln.Addr().String()
	if src != nil {
		src.setAPI(apiAddr)
	}
	clusterDeps.APIAddr = apiAddr
	if mesh != nil {
		mesh.Update()
	}

	fwd := &agentForwarder{}
	rt := &rpcRuntime{
		opt:      opt,
		dir:      clusterDeps.Dir,
		nodeID:   clusterDeps.NodeID,
		mgr:      mgr,
		st:       st,
		mesh:     mesh,
		src:      src,
		ready:    ready,
		degraded: degraded,
		fwd:      fwd,
		node:     clusterDeps.Control,
	}
	clusterDeps.OnReady = rt.startRPC
	if err := rt.startRPC(); err != nil {
		_ = ln.Close()
		rt.shutdown(context.Background())
		shutdownMesh(mesh)
		return fmt.Errorf("start rpc: %w", err)
	}

	var members func() []cluster.NodeSummary
	if mesh != nil {
		members = mesh.Members
	} else if src != nil {
		members = func() []cluster.NodeSummary { return []cluster.NodeSummary{src.Snapshot()} }
	}
	router := &api.Router{
		LocalID:      clusterDeps.NodeID,
		LocalHost:    clusterDeps.Hostname,
		Members:      members,
		LocalHasName: localHasNameFn(mgr),
	}

	var revs api.RevisionStore
	if st != nil {
		revs = st
	}
	srv, err := api.NewServer(api.Options{
		Addr:      opt.Listen,
		Mgr:       mgr,
		Logs:      logs,
		Store:     revs,
		Cluster:   clusterDeps,
		Degraded:  degraded,
		Ready:     ready,
		Started:   time.Now(),
		LocalOnly: false,
		LocalID:   clusterDeps.NodeID,
		Router:    router,
		Forward:   fwd,
	})
	if err != nil {
		_ = ln.Close()
		rt.shutdown(context.Background())
		shutdownMesh(mesh)
		return fmt.Errorf("new server: %w", err)
	}

	if opt.OnListen != nil {
		opt.OnListen(apiAddr)
	}

	if mgr != nil {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := mgr.Reconcile(ctx); err != nil {
						fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
					}
					if logs != nil {
						if _, err := logs.Protect(ctx); err != nil {
							fmt.Fprintf(os.Stderr, "protect: %v\n", err)
						}
					}
					_ = mgr.RotateLogs(ctx)
					if mesh != nil {
						mesh.Update()
					}
				}
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		rt.shutdown(shutCtx)
		shutdownMesh(mesh)
		return nil
	case err := <-errCh:
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		rt.shutdown(shutCtx)
		shutdownMesh(mesh)
		return err
	}
}

func shutdownMesh(mesh *cluster.Mesh) {
	if mesh == nil {
		return
	}
	_ = mesh.Leave(time.Second)
	_ = mesh.Shutdown()
}

func agentCertExists(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "agent.crt"))
	return err == nil && !st.IsDir()
}

func splitListen(addr string) (host string, port int, err error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("gossip address: %w", err)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("gossip port: %w", err)
	}
	return host, port, nil
}

// CheckListen refuses non-loopback binds unless insecure is set.
func CheckListen(addr string, insecure bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	if host == "" {
		// ":9000" binds all interfaces
		if !insecure {
			return errcode.E(errcode.INVALID, "non-loopback listen requires --insecure-listen")
		}
		fmt.Fprintln(os.Stderr, "warning: listening on all interfaces (--insecure-listen)")
		return nil
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if loopback {
		return nil
	}
	if !insecure {
		return errcode.E(errcode.INVALID, "non-loopback listen requires --insecure-listen")
	}
	fmt.Fprintf(os.Stderr, "warning: insecure listen on %s\n", addr)
	return nil
}

func lookupUser(name string) error {
	if name == "" {
		return nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return errcode.E(errcode.INVALID, "run_as_user")
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return errcode.E(errcode.INVALID, "run_as_user")
	}
	if uid != os.Getuid() && os.Getuid() != 0 {
		return errcode.E(errcode.INVALID, "run_as_user")
	}
	return nil
}
