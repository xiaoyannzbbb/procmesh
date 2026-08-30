package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/qleelulu/procmesh/internal/update/trust"
	"github.com/qleelulu/procmesh/internal/update/updater"
	"github.com/qleelulu/procmesh/internal/version"
)

const (
	managedInstallRoot = "/usr/local/lib/procmesh"
	managedUpdateRoot  = "/var/lib/procmesh/update"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("procmesh-updater", flag.ContinueOnError)
	flags.SetOutput(stderr)
	operationID := flags.String("operation-id", "", "canonical update operation UUID")
	recoverOperations := flags.Bool("recover", false, "recover durable operations after host restart")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*operationID == "") == !*recoverOperations {
		fmt.Fprintln(stderr, "exactly one of operation-id or recover is required; positional arguments are not accepted")
		return 2
	}
	if runtime.GOOS != "linux" {
		fmt.Fprintln(stderr, "managed ProcMesh updates require Linux systemd")
		return 1
	}
	keys, err := trust.DefaultKeyring()
	if err != nil {
		fmt.Fprintln(stderr, "trusted release keys are invalid")
		return 1
	}
	service, err := updater.NewSystemdService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options := updater.Options{
		InstallRoot:         managedInstallRoot,
		DataRoot:            managedUpdateRoot,
		Keys:                keys,
		OS:                  runtime.GOOS,
		Arch:                platformArch(),
		ProtocolVersion:     version.Protocol,
		ShimProtocolVersion: version.Protocol,
		Now:                 time.Now,
		Service:             service,
		Health:              updater.HTTPHealth{Agent: service},
	}
	if *recoverOperations {
		if err := updater.RecoverAll(ctx, options); err != nil {
			fmt.Fprintln(stderr, "recover durable updates failed")
			return 1
		}
		fmt.Fprintln(stdout, "durable update recovery completed")
		return 0
	}
	options.OperationID = *operationID
	result, err := updater.Execute(ctx, options)
	if err != nil {
		fmt.Fprintf(stderr, "update %s ended with status %s: %v\n", *operationID, result.Status, err)
		return 1
	}
	fmt.Fprintf(stdout, "update %s completed with status %s\n", result.OperationID, result.Status)
	return 0
}

func platformArch() string {
	if runtime.GOARCH == "arm" {
		return "armv7"
	}
	return runtime.GOARCH
}
