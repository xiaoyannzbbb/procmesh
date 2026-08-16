package api

import (
	"testing"

	pb "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewI18nError(t *testing.T) {
	err := NewI18nError(
		codes.NotFound,
		"PROCESS_NOT_FOUND",
		"Process nginx not found",
		map[string]string{"name": "nginx"},
	)

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}

	if st.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", st.Code())
	}

	details := st.Details()
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}

	detail, ok := details[0].(*pb.ErrorDetail)
	if !ok {
		t.Fatalf("expected ErrorDetail, got %T", details[0])
	}

	if detail.Code != "PROCESS_NOT_FOUND" {
		t.Errorf("expected PROCESS_NOT_FOUND, got %s", detail.Code)
	}

	if detail.Params["name"] != "nginx" {
		t.Errorf("expected name=nginx, got %s", detail.Params["name"])
	}
}

func TestProcessNotFoundError(t *testing.T) {
	err := ProcessNotFoundError("nginx")

	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", st.Code())
	}

	detail := st.Details()[0].(*pb.ErrorDetail)
	if detail.Code != "PROCESS_NOT_FOUND" {
		t.Errorf("expected PROCESS_NOT_FOUND, got %s", detail.Code)
	}
}

func TestConflictError(t *testing.T) {
	err := ConflictError(5, 3)

	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition, got %v", st.Code())
	}

	detail := st.Details()[0].(*pb.ErrorDetail)
	if detail.Code != "CONFLICT" {
		t.Errorf("expected CONFLICT, got %s", detail.Code)
	}

	if detail.Params["expected"] != "5" {
		t.Errorf("expected expected=5, got %s", detail.Params["expected"])
	}
}
