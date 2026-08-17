package api

import (
	"context"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.AlertServiceHandler = (*AlertAPI)(nil)

// AlertAPI is a stub AlertService handler.
type AlertAPI struct{}

func (*AlertAPI) ListAlerts(context.Context, *connect.Request[procmeshv1.ListAlertsRequest]) (*connect.Response[procmeshv1.ListAlertsResponse], error) {
	return nil, unimplemented()
}

func (*AlertAPI) GetAlert(context.Context, *connect.Request[procmeshv1.GetAlertRequest]) (*connect.Response[procmeshv1.GetAlertResponse], error) {
	return nil, unimplemented()
}

func (*AlertAPI) ListAlertChannels(context.Context, *connect.Request[procmeshv1.ListAlertChannelsRequest]) (*connect.Response[procmeshv1.ListAlertChannelsResponse], error) {
	return nil, unimplemented()
}

func (*AlertAPI) PutAlertChannel(context.Context, *connect.Request[procmeshv1.PutAlertChannelRequest]) (*connect.Response[procmeshv1.PutAlertChannelResponse], error) {
	return nil, unimplemented()
}

func (*AlertAPI) DeleteAlertChannel(context.Context, *connect.Request[procmeshv1.DeleteAlertChannelRequest]) (*connect.Response[procmeshv1.DeleteAlertChannelResponse], error) {
	return nil, unimplemented()
}

func (*AlertAPI) GetAlertPolicy(context.Context, *connect.Request[procmeshv1.GetAlertPolicyRequest]) (*connect.Response[procmeshv1.GetAlertPolicyResponse], error) {
	return nil, unimplemented()
}

func (*AlertAPI) PutAlertPolicy(context.Context, *connect.Request[procmeshv1.PutAlertPolicyRequest]) (*connect.Response[procmeshv1.PutAlertPolicyResponse], error) {
	return nil, unimplemented()
}
