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
	dataDir := fs.String("data-dir", "", "data directory (overrides agent.yaml data_dir)")
	listen := fs.String("listen", "", "HTTP listen address (overrides agent.yaml listen)")
	pprofListen := fs.String("pprof-listen", "", "pprof HTTP listen address (overrides agent.yaml pprof.listen; disabled by default)")
	rpcListen := fs.String("rpc", "", "RPC listen address (default 127.0.0.1:18683)")
	controlListen := fs.String("control", "", "control/raft listen address (default 127.0.0.1:18685)")
	breakGlassSocket := fs.String("break-glass-socket", "", "local break-glass Unix socket path")
	breakGlassGroup := fs.String("break-glass-group", "", "OS group allowed to use break-glass inspection")
	gossip := fs.String("gossip", "", "gossip listen address (default 127.0.0.1:18689)")
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

	logger, err := logging.New(stderr, *logFormat, *logLevel)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = agent.Run(ctx, agent.Options{
		DataDir:          *dataDir,
		Listen:           *listen,
		PprofListen:      *pprofListen,
		GossipListen:     *gossip,
		RPCListen:        *rpcListen,
		ControlListen:    *controlListen,
		BreakGlassSocket: *breakGlassSocket,
		BreakGlassGroup:  *breakGlassGroup,
		ShimBin:          *shimBin,
		InsecureListen:   *insecure,
		ConfigPath:       *config,
		Logger:           logger,
	})
	if err != nil {
		logger.Error("agent stopped", "error", err)
		return 1
	}

	return 0
}
