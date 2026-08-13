package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/qleelulu/procmesh/internal/agent"
)

func main() {
	dataDir := flag.String("data-dir", "", "data directory (required)")
	listen := flag.String("listen", "127.0.0.1:9000", "HTTP listen address")
	shimBin := flag.String("shim-bin", "", "path to procmesh-shim binary")
	flag.Parse()

	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "--data-dir is required")
		os.Exit(2)
	}

	if err := agent.Run(context.Background(), *dataDir, *listen, *shimBin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}