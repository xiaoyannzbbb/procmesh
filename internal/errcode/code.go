package errcode

import "errors"

type Code string

const (
	OK                   Code = "OK"
	CONFLICT             Code = "CONFLICT"
	UNAVAILABLE          Code = "UNAVAILABLE"
	TIMEOUT              Code = "TIMEOUT"
	RATE_LIMITED         Code = "RATE_LIMITED"
	INVALID_CREDENTIALS  Code = "INVALID_CREDENTIALS"
	ACCOUNT_LOCKED       Code = "ACCOUNT_LOCKED"
	DENIED               Code = "DENIED"
	DEGRADED             Code = "DEGRADED"
	DUPLICATE_NODE_ID    Code = "DUPLICATE_NODE_ID"
	INCOMPATIBLE_VERSION Code = "INCOMPATIBLE_VERSION"
	NOT_FOUND            Code = "NOT_FOUND"
	INVALID              Code = "INVALID"
)

type Error struct {
	Code Code
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return string(e.Code) + ": " + e.Msg
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func E(code Code, msg string) error {
	return &Error{Code: code, Msg: msg}
}

func Wrap(code Code, msg string, cause error) error {
	return &Error{Code: code, Msg: msg, Err: cause}
}

func Is(err error, code Code) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == code
}
