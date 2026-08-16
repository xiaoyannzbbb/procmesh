package api

import (
	"context"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

// BatchAPI is a stub; full implementation lands in Task 9.
var _ procmeshv1connect.BatchServiceHandler = (*BatchAPI)(nil)

type BatchAPI struct{}

func (s *BatchAPI) CreateBatch(context.Context, *connect.Request[procmeshv1.CreateBatchRequest]) (*connect.Response[procmeshv1.CreateBatchResponse], error) {
	return nil, unimplemented()
}

func (s *BatchAPI) GetBatch(context.Context, *connect.Request[procmeshv1.GetBatchRequest]) (*connect.Response[procmeshv1.GetBatchResponse], error) {
	return nil, unimplemented()
}

func (s *BatchAPI) ListBatches(context.Context, *connect.Request[procmeshv1.ListBatchesRequest]) (*connect.Response[procmeshv1.ListBatchesResponse], error) {
	return nil, unimplemented()
}

func (s *BatchAPI) RetryFailed(context.Context, *connect.Request[procmeshv1.RetryBatchRequest]) (*connect.Response[procmeshv1.RetryBatchResponse], error) {
	return nil, unimplemented()
}

func (s *BatchAPI) ReplayTimeout(context.Context, *connect.Request[procmeshv1.RetryBatchRequest]) (*connect.Response[procmeshv1.RetryBatchResponse], error) {
	return nil, unimplemented()
}

func (s *BatchAPI) ExportBatch(context.Context, *connect.Request[procmeshv1.ExportBatchRequest]) (*connect.Response[procmeshv1.ExportBatchResponse], error) {
	return nil, unimplemented()
}
