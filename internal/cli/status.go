package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runStatus(c *client, stdout io.Writer) error {
	resp, err := c.http.Get(strings.TrimRight(c.base, "/") + "/readyz")
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	state := "degraded"
	if resp.StatusCode == http.StatusOK {
		state = "ready"
	}

	listed, err := c.proc.ListProcesses(context.Background(), connect.NewRequest(&procmeshv1.ListProcessesRequest{}))
	if err != nil {
		return err
	}
	n := 0
	if listed.Msg != nil {
		n = len(listed.Msg.GetProcesses())
	}
	fmt.Fprintf(stdout, "%s\n%d\n", state, n)
	return nil
}
