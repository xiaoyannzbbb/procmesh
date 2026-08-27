package api

import (
	"net"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/errcode"
)

// ProcessRemotePolicy is the owner-local switch for remote spec create/update/delete.
// false (zero) keeps today's allow-remote default.
type ProcessRemotePolicy struct {
	DisableCreate bool
	DisableUpdate bool
	DisableDelete bool
}

func isLocalCLI(localOnly bool, header http.Header, remoteAddr string) bool {
	if localOnly {
		return false
	}
	if bearerToken(header) == "" {
		return false
	}
	if cookieValue(header, auth.CookieName) != "" {
		return false
	}
	return isLoopbackAddr(remoteAddr)
}

func isLoopbackAddr(addr string) bool {
	host := strings.TrimSpace(addr)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func rejectRemoteProcess(localOnly bool, header http.Header, remoteAddr string, disabled bool, action string) error {
	if !disabled {
		return nil
	}
	if isLocalCLI(localOnly, header, remoteAddr) {
		return nil
	}
	return ToConnect(errcode.E(errcode.DENIED, "remote process "+action+" is disabled on this agent; use local CLI on the owner"))
}

func rejectRemoteFromRequest(localOnly bool, req connect.AnyRequest, disabled bool, action string) error {
	if req == nil {
		return rejectRemoteProcess(localOnly, nil, "", disabled, action)
	}
	return rejectRemoteProcess(localOnly, req.Header(), req.Peer().Addr, disabled, action)
}
