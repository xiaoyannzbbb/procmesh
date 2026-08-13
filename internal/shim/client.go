package shim

import (
	"context"
	"fmt"
	"net"
	"time"

	shimpb "github.com/qleelulu/procmesh/proto/shim/v1"
	"google.golang.org/protobuf/proto"
)

// Client is a length-prefixed protobuf client for a procmesh-shim socket.
type Client struct {
	conn net.Conn
}

// Dial connects to a shim unix socket.
func Dial(socketPath string) (*Client, error) {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial shim: %w", err)
	}
	return &Client{conn: conn}, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Start sends a StartRequest and returns the StartResponse.
func (c *Client) Start(ctx context.Context, req *shimpb.StartRequest) (*shimpb.StartResponse, error) {
	env, err := c.roundTrip(ctx, &shimpb.Envelope{Body: &shimpb.Envelope_Start{Start: req}})
	if err != nil {
		return nil, err
	}
	if got := env.GetStartOk(); got != nil {
		return got, nil
	}
	return nil, fmt.Errorf("unexpected start response: %T", env.GetBody())
}

// Stop sends a StopRequest and returns the StopResponse.
func (c *Client) Stop(ctx context.Context, req *shimpb.StopRequest) (*shimpb.StopResponse, error) {
	env, err := c.roundTrip(ctx, &shimpb.Envelope{Body: &shimpb.Envelope_Stop{Stop: req}})
	if err != nil {
		return nil, err
	}
	if got := env.GetStopOk(); got != nil {
		return got, nil
	}
	return nil, fmt.Errorf("unexpected stop response: %T", env.GetBody())
}

// Status requests the child's current status.
func (c *Client) Status(ctx context.Context) (*shimpb.StatusResponse, error) {
	env, err := c.roundTrip(ctx, &shimpb.Envelope{Body: &shimpb.Envelope_Status{Status: &shimpb.StatusRequest{}}})
	if err != nil {
		return nil, err
	}
	if got := env.GetStatusOk(); got != nil {
		return got, nil
	}
	return nil, fmt.Errorf("unexpected status response: %T", env.GetBody())
}

// Signal sends a SignalRequest to the child.
func (c *Client) Signal(ctx context.Context, req *shimpb.SignalRequest) (*shimpb.SignalResponse, error) {
	env, err := c.roundTrip(ctx, &shimpb.Envelope{Body: &shimpb.Envelope_Signal{Signal: req}})
	if err != nil {
		return nil, err
	}
	if got := env.GetSignalOk(); got != nil {
		return got, nil
	}
	return nil, fmt.Errorf("unexpected signal response: %T", env.GetBody())
}

// Wait blocks until the child exits or ctx is done.
func (c *Client) Wait(ctx context.Context) (*shimpb.WaitResponse, error) {
	env, err := c.roundTrip(ctx, &shimpb.Envelope{Body: &shimpb.Envelope_Wait{Wait: &shimpb.WaitRequest{}}})
	if err != nil {
		return nil, err
	}
	if got := env.GetWaitOk(); got != nil {
		return got, nil
	}
	return nil, fmt.Errorf("unexpected wait response: %T", env.GetBody())
}

func (c *Client) roundTrip(ctx context.Context, req *shimpb.Envelope) (*shimpb.Envelope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
		defer c.conn.SetDeadline(time.Time{})
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	if err := WriteFrame(c.conn, payload); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	raw, err := ReadFrame(c.conn)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var resp shimpb.Envelope
	if err := proto.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &resp, nil
}
