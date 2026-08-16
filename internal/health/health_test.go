package health_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/health"
	"github.com/qleelulu/procmesh/internal/process"
)

func TestCheck_HTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	err := health.Check(context.Background(), process.HealthCheckSpec{Type: "http", URL: srv.URL, ExpectedStatus: 200, Timeout: time.Second}, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = health.Check(context.Background(), process.HealthCheckSpec{Type: "http", URL: srv.URL, ExpectedStatus: 204, Timeout: time.Second}, 0)
	if err == nil {
		t.Fatal("expected status mismatch")
	}
}

func TestTracker_Thresholds(t *testing.T) {
	tr := health.NewTracker(process.HealthCheckSpec{FailureThreshold: 3, SuccessThreshold: 2})
	now := time.Now()
	if tr.Observe(errors.New("x"), now) != process.HealthUnknown {
		t.Fatal()
	}
	if tr.Observe(errors.New("x"), now) != process.HealthUnknown {
		t.Fatal()
	}
	if tr.Observe(errors.New("x"), now) != process.HealthUnhealthy {
		t.Fatal()
	}
}

func TestTracker_SuccessThreshold(t *testing.T) {
	tr := health.NewTracker(process.HealthCheckSpec{FailureThreshold: 1, SuccessThreshold: 2})
	now := time.Now()
	if tr.Observe(errors.New("x"), now) != process.HealthUnhealthy {
		t.Fatal("first failure")
	}
	if tr.Observe(nil, now) != process.HealthUnhealthy {
		t.Fatal("one success stays unhealthy")
	}
	if tr.Observe(nil, now) != process.HealthHealthy {
		t.Fatal("two successes")
	}
}

func TestTracker_SuccessResetsFailures(t *testing.T) {
	tr := health.NewTracker(process.HealthCheckSpec{FailureThreshold: 3, SuccessThreshold: 1})
	now := time.Now()
	if tr.Observe(errors.New("x"), now) != process.HealthUnknown {
		t.Fatal()
	}
	if tr.Observe(errors.New("x"), now) != process.HealthUnknown {
		t.Fatal()
	}
	if tr.Observe(nil, now) != process.HealthHealthy {
		t.Fatal("success should reset fail streak")
	}
	if tr.Observe(errors.New("x"), now) != process.HealthHealthy {
		t.Fatal("single failure after healthy")
	}
	if tr.Observe(errors.New("x"), now) != process.HealthHealthy {
		t.Fatal()
	}
	if tr.Observe(errors.New("x"), now) != process.HealthUnhealthy {
		t.Fatal()
	}
}

func TestCheck_Alive(t *testing.T) {
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "alive", Timeout: time.Second}, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := health.Check(context.Background(), process.HealthCheckSpec{}, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "alive"}, 0); err == nil {
		t.Fatal("expected dead pid")
	}
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "alive"}, 1<<30); err == nil {
		t.Fatal("expected missing pid")
	}
}

func TestCheck_TCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "tcp", Address: ln.Addr().String(), Timeout: time.Second}, 0); err != nil {
		t.Fatal(err)
	}
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "tcp", Address: "127.0.0.1:1", Timeout: 50 * time.Millisecond}, 0); err == nil {
		t.Fatal("expected refused")
	}
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "tcp", Timeout: time.Second}, 0); err == nil {
		t.Fatal("expected empty address")
	}
}

func TestCheck_Exec(t *testing.T) {
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skip(err)
	}
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip(err)
	}
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip(err)
	}
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "exec", Command: trueBin, Timeout: time.Second}, 0); err != nil {
		t.Fatal(err)
	}
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "exec", Command: falseBin, Timeout: time.Second}, 0); err == nil {
		t.Fatal("expected exec failure")
	}
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "exec", Command: sleepBin, Args: []string{"5"}, Timeout: 50 * time.Millisecond}, 0); err == nil {
		t.Fatal("expected timeout")
	}
}

func TestCheck_HTTPOnlyHTTPSchemes(t *testing.T) {
	err := health.Check(context.Background(), process.HealthCheckSpec{Type: "http", URL: "ftp://127.0.0.1/", Timeout: time.Second}, 0)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestCheck_HTTPRedirectLimit(t *testing.T) {
	var srv *httptest.Server
	n := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n <= 6 {
			http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	err := health.Check(context.Background(), process.HealthCheckSpec{Type: "http", URL: srv.URL, ExpectedStatus: 200, Timeout: time.Second}, 0)
	if err == nil {
		t.Fatal("expected redirect limit")
	}
}

func TestCheck_HTTPFollowsFiveRedirects(t *testing.T) {
	var srv *httptest.Server
	n := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n <= 5 {
			http.Redirect(w, r, srv.URL+"/n", http.StatusFound)
			return
		}
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	if err := health.Check(context.Background(), process.HealthCheckSpec{Type: "http", URL: srv.URL, ExpectedStatus: 200, Timeout: time.Second}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestCheck_ExecNotShell(t *testing.T) {
	err := health.Check(context.Background(), process.HealthCheckSpec{Type: "exec", Command: "true && false", Timeout: time.Second}, 0)
	if err == nil {
		t.Fatal("must not invoke a shell")
	}
}

func TestCheck_UnknownType(t *testing.T) {
	err := health.Check(context.Background(), process.HealthCheckSpec{Type: "icmp", Timeout: time.Second}, 0)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}
