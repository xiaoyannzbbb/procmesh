package rpc

import "net/http"

const (
	HeaderTargetNode = "Procmesh-Target-Node"
	HeaderSourceNode = "Procmesh-Source-Node"
)

func TargetOf(h http.Header) string { return h.Get(HeaderTargetNode) }
func SourceOf(h http.Header) string { return h.Get(HeaderSourceNode) }

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
