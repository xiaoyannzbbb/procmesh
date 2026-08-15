package api

import (
	"context"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.MetricsServiceHandler = (*MetricsAPI)(nil)

type MetricsAPI struct{}

func (s *MetricsAPI) GetAgentMetrics(context.Context, *connect.Request[procmeshv1.GetAgentMetricsRequest]) (*connect.Response[procmeshv1.GetAgentMetricsResponse], error) {
	return nil, unimplemented()
}

func (s *MetricsAPI) GetProcessMetrics(context.Context, *connect.Request[procmeshv1.GetProcessMetricsRequest]) (*connect.Response[procmeshv1.GetProcessMetricsResponse], error) {
	return nil, unimplemented()
}
