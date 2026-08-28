package api

import (
	"errors"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

// CodeOf extracts the procmesh errcode from err, or empty for non-errcode errors.
func CodeOf(err error) errcode.Code {
	var e *errcode.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func unimplemented() error {
	return ToConnect(errcode.E(errcode.UNAVAILABLE, "not implemented"))
}

// ToConnect maps err to a Connect error with ErrorInfo detail.
// nil is returned unchanged.
func ToConnect(err error) error {
	return toConnectWithDetailCode(err, string(CodeOf(err)))
}

func toConnectWithCapturedSnapshot(err error, snapshotID, sha256 string) error {
	if err == nil {
		return nil
	}
	converted := ToConnect(err)
	if snapshotID == "" && sha256 == "" {
		return converted
	}
	var ce *connect.Error
	if !errors.As(converted, &ce) {
		return converted
	}
	detail, detailErr := connect.NewErrorDetail(&procmeshv1.ReplicateSnapshotResponse{SnapshotId: snapshotID, Sha256: sha256})
	if detailErr == nil {
		ce.AddDetail(detail)
	}
	return ce
}

func toConnectWithDetailCode(err error, detailCode string) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	return newConnectWithDetailCode(err, detailCode)
}

func newConnectWithDetailCode(err error, detailCode string) error {
	c := CodeOf(err)
	ce := connect.NewError(toConnectCode(c), err)
	detail, detailErr := connect.NewErrorDetail(&procmeshv1.ErrorInfo{
		Code:    detailCode,
		Message: err.Error(),
	})
	if detailErr == nil {
		ce.AddDetail(detail)
	}
	return ce
}

func toConnectCode(c errcode.Code) connect.Code {
	switch c {
	case errcode.CONFLICT:
		return connect.CodeFailedPrecondition
	case errcode.NOT_FOUND:
		return connect.CodeNotFound
	case errcode.INVALID:
		return connect.CodeInvalidArgument
	case errcode.DEGRADED:
		return connect.CodeUnavailable
	case errcode.UNAVAILABLE:
		return connect.CodeUnavailable
	case errcode.TIMEOUT:
		return connect.CodeDeadlineExceeded
	case errcode.RATE_LIMITED:
		return connect.CodeResourceExhausted
	case errcode.INVALID_CREDENTIALS, errcode.ACCOUNT_LOCKED, errcode.DENIED:
		return connect.CodePermissionDenied
	case errcode.DUPLICATE_NODE_ID:
		return connect.CodeAlreadyExists
	case errcode.INCOMPATIBLE_VERSION:
		return connect.CodeFailedPrecondition
	default:
		return connect.CodeUnknown
	}
}
