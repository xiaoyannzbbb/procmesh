package api

import (
	"fmt"
	"strconv"

	pb "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewI18nError creates a gRPC error with ErrorDetail for i18n support.
func NewI18nError(grpcCode codes.Code, errCode, message string, params map[string]string) error {
	detail := &pb.ErrorDetail{
		Code:    errCode,
		Message: message,
		Params:  params,
	}

	st := status.New(grpcCode, message)
	st, _ = st.WithDetails(detail)
	return st.Err()
}

// ProcessNotFoundError returns a structured error for process not found.
func ProcessNotFoundError(name string) error {
	return NewI18nError(
		codes.NotFound,
		"PROCESS_NOT_FOUND",
		fmt.Sprintf("Process %s not found", name),
		map[string]string{"name": name},
	)
}

// ConflictError returns a structured error for configuration conflicts.
func ConflictError(expected, actual int64) error {
	return NewI18nError(
		codes.FailedPrecondition,
		"CONFLICT",
		fmt.Sprintf("Configuration conflict: expected revision %d, got %d", expected, actual),
		map[string]string{
			"expected": strconv.FormatInt(expected, 10),
			"actual":   strconv.FormatInt(actual, 10),
		},
	)
}

// UnavailableError returns a structured error for unavailable agents.
func UnavailableError(agent string) error {
	return NewI18nError(
		codes.Unavailable,
		"UNAVAILABLE",
		fmt.Sprintf("Target agent %s is unavailable", agent),
		map[string]string{"agent": agent},
	)
}

// PermissionDeniedError returns a structured error for permission denied.
func PermissionDeniedError(action, permission string) error {
	return NewI18nError(
		codes.PermissionDenied,
		"DENIED",
		fmt.Sprintf("Permission denied: %s requires %s", action, permission),
		map[string]string{
			"action":     action,
			"permission": permission,
		},
	)
}

// TimeoutError returns a structured error for operation timeout.
func TimeoutError(seconds int) error {
	return NewI18nError(
		codes.DeadlineExceeded,
		"TIMEOUT",
		fmt.Sprintf("Operation timed out after %ds", seconds),
		map[string]string{"seconds": strconv.Itoa(seconds)},
	)
}
