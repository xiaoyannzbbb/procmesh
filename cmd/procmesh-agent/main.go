package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/qleelulu/procmesh/internal/agent"
)

func main() {
	dataDir := flag.String("data-dir", "", "data directory (required)")
	listen := flag.String("listen", "127.0.0.1:9000", "HTTP listen address")
	shimBin := flag.String("shim-bin", "", "path to procmesh-shim binary")
	insecure := flag.Bool("insecure-listen", false, "allow non-loopback listen (logs a warning)")
	flag.Parse()

	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "--data-dir is required")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := agent.Run(ctx, agent.Options{
		DataDir:        *dataDir,
		Listen:         *listen,
		ShimBin:        *shimBin,
		InsecureListen: *insecure,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
