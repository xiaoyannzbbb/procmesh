package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/localhttp"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

// Options is the procmesh-agent runtime configuration.
type Options struct {
	DataDir        string
	Listen         string
	ShimBin        string
	InsecureListen bool
	OnListen       func(addr string)
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
	st, err := store.Open(layout.Store)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	degraded := false
	if err := st.IntegrityCheck(ctx); err != nil {
		degraded = true
		fmt.Fprintf(os.Stderr, "integrity check: %v\n", err)
	}

	if err := st.SetBootID(ctx, paths.CurrentBootID()); err != nil {
		return fmt.Errorf("set boot id: %w", err)
	}

	logs := &logmgr.Manager{Root: layout.Root, Now: time.Now}
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
	srv, err := localhttp.NewServerOpts(mgr, logs, opt.Listen, degraded, ready)
	if err != nil {
		return fmt.Errorf("new server: %w", err)
	}

	ln, err := net.Listen("tcp", opt.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if opt.OnListen != nil {
		opt.OnListen(ln.Addr().String())
	}

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
				if _, err := logs.Protect(ctx); err != nil {
					fmt.Fprintf(os.Stderr, "protect: %v\n", err)
				}
				_ = mgr.RotateLogs(ctx)
			}
		}
	}()

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
		return nil
	case err := <-errCh:
		return err
	}
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
