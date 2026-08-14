package cli

import (
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type client struct {
	base     string
	http     *http.Client
	proc     procmeshv1connect.ProcessServiceClient
	cfg      procmeshv1connect.ConfigServiceClient
	logs     procmeshv1connect.LogServiceClient
	opID     string
	operator string
}

func newClient(server, opID, operator string) *client {
	base := normalizeServer(server)
	hc := http.DefaultClient
	return &client{
		base:     base,
		http:     hc,
		proc:     procmeshv1connect.NewProcessServiceClient(hc, base),
		cfg:      procmeshv1connect.NewConfigServiceClient(hc, base),
		logs:     procmeshv1connect.NewLogServiceClient(hc, base),
		opID:     opID,
		operator: operator,
	}
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
