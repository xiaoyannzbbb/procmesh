package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/qleelulu/procmesh/internal/shim"
)

func main() {
	socket := flag.String("socket", "", "unix socket path (required)")
	instanceID := flag.String("instance-id", "", "instance id (required)")
	flag.Parse()
	if *socket == "" || *instanceID == "" {
		fmt.Fprintln(os.Stderr, "--socket and --instance-id are required")
		os.Exit(2)
	}
	if err := shim.Serve(context.Background(), *socket); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
