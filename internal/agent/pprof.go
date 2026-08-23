package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	stdpprof "net/http/pprof"
	"time"
)

type pprofHTTPServer struct {
	http     *http.Server
	listener net.Listener
}

func newPprofServer(addr string) (*pprofHTTPServer, error) {
	if addr == "" {
		return nil, nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pprof listen: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", stdpprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", stdpprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", stdpprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", stdpprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", stdpprof.Trace)

	return &pprofHTTPServer{
		http: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       30 * time.Second,
		},
		listener: ln,
	}, nil
}

func (s *pprofHTTPServer) Addr() string {
	return s.listener.Addr().String()
}

func (s *pprofHTTPServer) Serve() error {
	err := s.http.Serve(s.listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *pprofHTTPServer) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
