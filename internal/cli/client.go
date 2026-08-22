package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

const httpTimeout = 5 * time.Second

type client struct {
	base     string
	http     *http.Client
	proc     procmeshv1connect.ProcessServiceClient
	cfg      procmeshv1connect.ConfigServiceClient
	logs     procmeshv1connect.LogServiceClient
	node     procmeshv1connect.NodeServiceClient
	cluster  procmeshv1connect.ClusterServiceClient
	auth     procmeshv1connect.AuthServiceClient
	user     procmeshv1connect.UserServiceClient
	role     procmeshv1connect.RoleServiceClient
	group    procmeshv1connect.GroupServiceClient
	batch    procmeshv1connect.BatchServiceClient
	metrics  procmeshv1connect.MetricsServiceClient
	alert    procmeshv1connect.AlertServiceClient
	backup   procmeshv1connect.BackupServiceClient
	opID     string
	operator string
}

func newClient(server, opID, operator, node, authToken string) *client {
	base := normalizeServer(server)
	hc := &http.Client{Timeout: httpTimeout}
	opts := []connect.ClientOption{}
	var interceptors []connect.Interceptor
	if node != "" {
		interceptors = append(interceptors, targetNodeInterceptor(node))
	}
	if authToken != "" {
		interceptors = append(interceptors, bearerInterceptor(authToken))
	}
	if len(interceptors) > 0 {
		opts = append(opts, connect.WithInterceptors(interceptors...))
	}
	return &client{
		base:     base,
		http:     hc,
		proc:     procmeshv1connect.NewProcessServiceClient(hc, base, opts...),
		cfg:      procmeshv1connect.NewConfigServiceClient(hc, base, opts...),
		logs:     procmeshv1connect.NewLogServiceClient(hc, base, opts...),
		node:     procmeshv1connect.NewNodeServiceClient(hc, base, opts...),
		cluster:  procmeshv1connect.NewClusterServiceClient(hc, base, opts...),
		auth:     procmeshv1connect.NewAuthServiceClient(hc, base, opts...),
		user:     procmeshv1connect.NewUserServiceClient(hc, base, opts...),
		role:     procmeshv1connect.NewRoleServiceClient(hc, base, opts...),
		group:    procmeshv1connect.NewGroupServiceClient(hc, base, opts...),
		batch:    procmeshv1connect.NewBatchServiceClient(hc, base, opts...),
		metrics:  procmeshv1connect.NewMetricsServiceClient(hc, base, opts...),
		alert:    procmeshv1connect.NewAlertServiceClient(hc, base, opts...),
		backup:   procmeshv1connect.NewBackupServiceClient(hc, base, opts...),
		opID:     opID,
		operator: operator,
	}
}

func newBreakGlassClient(socketPath, opID, operator string) *client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	hc := &http.Client{Timeout: httpTimeout, Transport: transport}
	const base = "http://procmesh.local"
	return &client{
		base:     base,
		http:     hc,
		proc:     procmeshv1connect.NewProcessServiceClient(hc, base),
		logs:     procmeshv1connect.NewLogServiceClient(hc, base),
		opID:     opID,
		operator: operator,
	}
}

// targetNodeInterceptor sets Procmesh-Target-Node on unary and streaming client RPCs.
type targetNodeInterceptor string

func (n targetNodeInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set(rpc.HeaderTargetNode, string(n))
		return next(ctx, req)
	}
}

func (n targetNodeInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set(rpc.HeaderTargetNode, string(n))
		return conn
	}
}

func (n targetNodeInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

type bearerInterceptor string

func (t bearerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+string(t))
		return next(ctx, req)
	}
}

func (t bearerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+string(t))
		return conn
	}
}

func (t bearerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (c *client) meta() *procmeshv1.MutationMeta {
	return &procmeshv1.MutationMeta{OperationId: c.opID, Operator: c.operator}
}

func normalizeServer(s string) string {
	if s == "" {
		s = defaultServer
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "http://" + s
	}
	return s
}

func formatErr(err error) string {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err.Error()
	}
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		info, ok := msg.(*procmeshv1.ErrorInfo)
		if !ok {
			continue
		}
		return formatErrorInfo(info)
	}
	return ce.Error()
}

func formatErrorInfo(info *procmeshv1.ErrorInfo) string {
	code := info.GetCode()
	msg := info.GetMessage()
	if code == "" {
		return msg
	}
	if msg == "" {
		return code
	}
	if msg == code || strings.HasPrefix(msg, code+":") {
		return msg
	}
	return code + ": " + msg
}
