package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/qleelulu/procmesh/internal/agent"
	"github.com/qleelulu/procmesh/internal/logging"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("procmesh-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory (required)")
	listen := fs.String("listen", "127.0.0.1:9000", "HTTP listen address")
	rpcListen := fs.String("rpc", "", "RPC listen address (default 127.0.0.1:9001)")
	controlListen := fs.String("control", "", "control/raft listen address (default 127.0.0.1:9002)")
	gossip := fs.String("gossip", "", "gossip listen address (default 127.0.0.1:7946)")
	shimBin := fs.String("shim-bin", "", "path to procmesh-shim binary")
	insecure := fs.Bool("insecure-listen", false, "allow non-loopback listen (logs a warning)")
	config := fs.String("config", "", "agent.yaml path (optional)")
	logFormat := fs.String("log-format", "text", "log format: text or json")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, or error")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *dataDir == "" {
		fmt.Fprintln(stderr, "--data-dir is required")
		return 2
	}

	logger, err := logging.New(stderr, *logFormat, *logLevel)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = agent.Run(ctx, agent.Options{
		DataDir:        *dataDir,
		Listen:         *listen,
		GossipListen:   *gossip,
		RPCListen:      *rpcListen,
		ControlListen:  *controlListen,
		ShimBin:        *shimBin,
		InsecureListen: *insecure,
		ConfigPath:     *config,
		Logger:         logger,
	})
	if err != nil {
		logger.Error("agent stopped", "error", err)
		return 1
	}

	return 0
}
