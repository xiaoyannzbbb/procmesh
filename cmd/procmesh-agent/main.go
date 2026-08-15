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
	rpcListen := flag.String("rpc", "", "RPC listen address (default 127.0.0.1:9001)")
	gossip := flag.String("gossip", "", "gossip listen address (default 127.0.0.1:7946)")
	shimBin := flag.String("shim-bin", "", "path to procmesh-shim binary")
	insecure := flag.Bool("insecure-listen", false, "allow non-loopback listen (logs a warning)")
	config := flag.String("config", "", "agent.yaml path (optional)")
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
		GossipListen:   *gossip,
		RPCListen:      *rpcListen,
		ShimBin:        *shimBin,
		InsecureListen: *insecure,
		ConfigPath:     *config,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
