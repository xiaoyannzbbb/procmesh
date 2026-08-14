package api

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"google.golang.org/protobuf/proto"
)

func TestToConnect_Nil(t *testing.T) {
	if ToConnect(nil) != nil {
		t.Fatal("nil")
	}
}

func TestToConnect_ConflictDetail(t *testing.T) {
	err := ToConnect(errcode.E(errcode.CONFLICT, "revision mismatch"))
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("%T", err)
	}
	if ce.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("code=%v", ce.Code())
	}
	if len(ce.Details()) == 0 {
		t.Fatal("missing detail")
	}
	msg, err := ce.Details()[0].Value()
	if err != nil {
		t.Fatal(err)
	}
	info, ok := msg.(*procmeshv1.ErrorInfo)
	if !ok || info.GetCode() != "CONFLICT" {
		t.Fatalf("%v", msg)
	}
	if !proto.Equal(&procmeshv1.ErrorInfo{Code: "CONFLICT", Message: "CONFLICT: revision mismatch"}, info) &&
		info.GetMessage() == "" {
		t.Fatalf("empty message: %+v", info)
	}
}

func TestToConnect_Table(t *testing.T) {
	cases := []struct {
		in   errcode.Code
		want connect.Code
	}{
		{errcode.NOT_FOUND, connect.CodeNotFound},
		{errcode.INVALID, connect.CodeInvalidArgument},
		{errcode.DEGRADED, connect.CodeUnavailable},
		{errcode.UNAVAILABLE, connect.CodeUnavailable},
		{errcode.TIMEOUT, connect.CodeDeadlineExceeded},
		{errcode.DENIED, connect.CodePermissionDenied},
		{errcode.DUPLICATE_NODE_ID, connect.CodeAlreadyExists},
		{errcode.INCOMPATIBLE_VERSION, connect.CodeFailedPrecondition},
	}
	for _, tc := range cases {
		err := ToConnect(errcode.E(tc.in, "x"))
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != tc.want {
			t.Fatalf("%s -> %v", tc.in, err)
		}
	}
}

func TestToConnect_PlainErrorUnknown(t *testing.T) {
	err := ToConnect(errors.New("boom"))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnknown {
		t.Fatalf("%v", err)
	}
}
