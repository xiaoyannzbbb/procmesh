package api

import (
	"context"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.AuditServiceHandler = (*AuditAPI)(nil)

type AuditAPI struct{}

func (s *AuditAPI) ListAudit(context.Context, *connect.Request[procmeshv1.ListAuditRequest]) (*connect.Response[procmeshv1.ListAuditResponse], error) {
	return nil, unimplemented()
}
