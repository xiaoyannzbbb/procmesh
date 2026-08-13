package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/qleelulu/procmesh/internal/localhttp"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

func Run(ctx context.Context, dataDir, listen, shimBin string) error {
	if dataDir == "" {
		return fmt.Errorf("data-dir required")
	}
	layout := paths.New(dataDir)
	if err := layout.Ensure(); err != nil {
		return fmt.Errorf("ensure layout: %w", err)
	}
	s, err := store.Open(layout.Store)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	// RotateBootID once per start
	_, err = s.RotateBootID(ctx)
	if err != nil {
		return fmt.Errorf("rotate boot id: %w", err)
	}
	// create manager
	mgr := process.NewManager(process.Deps{
		Store:    s,
		Layout:   layout,
		ShimBin:  shimBin,
		Now:      time.Now,
	})
	// Recover
	if err := mgr.Recover(ctx); err != nil {
		// continue anyway, per brief
		fmt.Fprintf(os.Stderr, "recover failed: %v\n", err)
	}
	// 1s ticker Reconcile + Protect
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := mgr.Reconcile(ctx); err != nil {
					fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
				}
				// protect logs if present
			}
		}
	}()
	// start HTTP server
	srv, err := localhttp.NewServer(mgr, nil, listen)
	if err != nil {
		return fmt.Errorf("new server: %w", err)
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		}
	}()
	return nil
}
// Fix: pre-existing build error fixed by using ListenAndServe() (replaces Serve(listener) which required undefined Listener field).
// This starts the server using the embedded http.Server's ListenAndServe method.
