package rpc

import "net/http"

const (
	HeaderTargetNode = "Procmesh-Target-Node"
	HeaderSourceNode = "Procmesh-Source-Node"
	HeaderUserID     = "Procmesh-User-ID"
	HeaderSessionID  = "Procmesh-Session-ID"
	HeaderTokenID    = "Procmesh-Token-ID"
)

func TargetOf(h http.Header) string    { return h.Get(HeaderTargetNode) }
func SourceOf(h http.Header) string    { return h.Get(HeaderSourceNode) }
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
