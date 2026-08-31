package api

import (
	"context"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/update"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.UpdateServiceHandler = (*UpdateAPI)(nil)

// LatestChecker is satisfied by *update.Checker.
type LatestChecker interface {
	CheckLatest(ctx context.Context, refresh bool) (update.Result, error)
}

// UpdateAPI serves UpdateService RPCs.
type UpdateAPI struct {
	Auth    *auth.Service
	Checker LatestChecker
}

func (s *UpdateAPI) CheckLatest(ctx context.Context, req *connect.Request[procmeshv1.CheckLatestRequest]) (*connect.Response[procmeshv1.CheckLatestResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterRead, "", false, true); err != nil {
		return nil, err
	}
	if s.Checker == nil {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "update checker unavailable"))
	}
	res, err := s.Checker.CheckLatest(ctx, req.Msg.GetRefresh())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.CheckLatestResponse{
		Repository:    res.Pin.Repository,
		Tag:           res.Pin.Tag,
		Checksums:     res.Pin.Checksums,
		CheckedUnixMs: res.CheckedUnixMs,
		FromCache:     res.FromCache,
		CheckError:    res.CheckError,
		ErrorMessage:  res.ErrorMessage,
	}), nil
}
