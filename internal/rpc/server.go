package rpc

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"

	"github.com/qleelulu/procmesh/internal/control"
)

// Server is an mTLS HTTP server for Agent-to-Agent Connect RPC.
type Server struct {
	http *http.Server
	addr string
}

// NewServer builds an mTLS server with the given handler.
// Handler is injected by the caller (e.g. LocalOnly Process/Config/Log/Audit/Metrics Connect handlers).
func NewServer(addr string, creds control.AgentCreds, clusterID string, h http.Handler, revoked func(serial string) bool) (*Server, error) {
	tlsCfg, err := ServerTLS(creds, clusterID, revoked)
	if err != nil {
		return nil, err
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			r = r.WithContext(WithTLSState(r.Context(), *r.TLS))
		}
		h.ServeHTTP(w, r)
	})
	s := &http.Server{Addr: addr, Handler: handler, TLSConfig: tlsCfg}
	return &Server{http: s, addr: addr}, nil
}

// Serve accepts connections on l with mTLS and serves HTTP.
func (s *Server) Serve(l net.Listener) error {
	tlsLn := tls.NewListener(l, s.http.TLSConfig)
	s.addr = tlsLn.Addr().String()
	return s.http.Serve(tlsLn)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// Addr returns the listen address (updated after Serve starts).
func (s *Server) Addr() string {
	return s.addr
}
