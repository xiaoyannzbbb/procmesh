package rpc

import (
	"context"
	"errors"
	"os"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/errcode"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

// MapDialError maps dial/connect failures to TIMEOUT or UNAVAILABLE.
// Existing *errcode.Error values are returned unchanged.
func MapDialError(err error) error {
	if err == nil {
		return nil
	}
	var ee *errcode.Error
	if errors.As(err, &ee) {
		return err
	}
	if isTimeout(err) {
		return errcode.E(errcode.TIMEOUT, "rpc timed out")
	}
	return errcode.E(errcode.UNAVAILABLE, "owner unreachable")
}

// MapCallError maps RPC call errors to errcode values.
// Existing *errcode.Error and Connect errors with ErrorInfo are returned as-is.
// DeadlineExceeded → TIMEOUT; Unavailable/Unknown without ErrorInfo → UNAVAILABLE.
func MapCallError(err error) error {
	if err == nil {
		return nil
	}
	var ee *errcode.Error
	if errors.As(err, &ee) {
		return err
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		if hasErrorInfo(ce) {
			return err
		}
		switch ce.Code() {
		case connect.CodeDeadlineExceeded:
			return errcode.E(errcode.TIMEOUT, "rpc timed out")
		case connect.CodeUnavailable, connect.CodeUnknown:
			return errcode.E(errcode.UNAVAILABLE, "owner unreachable")
		default:
			return err
		}
	}
	if isTimeout(err) {
		return errcode.E(errcode.TIMEOUT, "rpc timed out")
	}
	return err
}

func isTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded)
}

func hasErrorInfo(ce *connect.Error) bool {
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		if _, ok := msg.(*procmeshv1.ErrorInfo); ok {
			return true
		}
	}
	return false
}
