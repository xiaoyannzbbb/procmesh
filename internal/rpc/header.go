package rpc

import "net/http"

const (
	HeaderTargetNode       = "Procmesh-Target-Node"
	HeaderSourceNode       = "Procmesh-Source-Node"
	HeaderLoginHop         = "Procmesh-Login-Hop"
	HeaderUserID           = "Procmesh-User-ID"
	HeaderSessionID        = "Procmesh-Session-ID"
	HeaderTokenID          = "Procmesh-Token-ID"
	HeaderBreakGlassReason = "Procmesh-Break-Glass-Reason"
)

func TargetOf(h http.Header) string    { return h.Get(HeaderTargetNode) }
func SourceOf(h http.Header) string    { return h.Get(HeaderSourceNode) }
func LoginHopOf(h http.Header) string  { return h.Get(HeaderLoginHop) }
func UserIDOf(h http.Header) string    { return h.Get(HeaderUserID) }
func SessionIDOf(h http.Header) string { return h.Get(HeaderSessionID) }
func TokenIDOf(h http.Header) string   { return h.Get(HeaderTokenID) }

func SetTarget(h http.Header, nodeID string) {
	if nodeID == "" {
		return
	}
	h.Set(HeaderTargetNode, nodeID)
}

func SetSource(h http.Header, nodeID string) {
	if nodeID == "" {
		return
	}
	h.Set(HeaderSourceNode, nodeID)
}

func SetLoginHop(h http.Header, hop string) {
	if hop == "" {
		h.Del(HeaderLoginHop)
		return
	}
	h.Set(HeaderLoginHop, hop)
}

func SetUserID(h http.Header, userID string) {
	if userID == "" {
		return
	}
	h.Set(HeaderUserID, userID)
}

func SetSessionID(h http.Header, sessionID string) {
	if sessionID == "" {
		return
	}
	h.Set(HeaderSessionID, sessionID)
}

func SetTokenID(h http.Header, tokenID string) {
	if tokenID == "" {
		return
	}
	h.Set(HeaderTokenID, tokenID)
}

// CopyIdentity 用 src 的 User/Session/Token 覆盖 dst（缺省则清空）。
func CopyIdentity(dst, src http.Header) {
	if dst == nil {
		return
	}
	dst.Del(HeaderUserID)
	dst.Del(HeaderSessionID)
	dst.Del(HeaderTokenID)
	if src == nil {
		return
	}
	SetUserID(dst, UserIDOf(src))
	SetSessionID(dst, SessionIDOf(src))
	SetTokenID(dst, TokenIDOf(src))
}
