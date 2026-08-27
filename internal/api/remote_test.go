package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/auth"
)

func TestIsLoopbackAddr(t *testing.T) {
	if !isLoopbackAddr("127.0.0.1:18680") || !isLoopbackAddr("[::1]:9") {
		t.Fatal("loopback")
	}
	if isLoopbackAddr("10.0.0.8:18680") || isLoopbackAddr("") || isLoopbackAddr("hostname:80") {
		t.Fatal("non-loopback")
	}
}

func TestIsLocalCLI(t *testing.T) {
	cli := http.Header{}
	cli.Set("Authorization", "Bearer tok")
	web := http.Header{}
	web.Set("Cookie", auth.CookieName+"=sid")
	both := http.Header{}
	both.Set("Authorization", "Bearer tok")
	both.Set("Cookie", auth.CookieName+"=sid")

	if !isLocalCLI(false, cli, "127.0.0.1:9") {
		t.Fatal("loopback bearer is local CLI")
	}
	if isLocalCLI(true, cli, "127.0.0.1:9") {
		t.Fatal("owner hop is remote")
	}
	if isLocalCLI(false, web, "127.0.0.1:9") {
		t.Fatal("cookie is web")
	}
	if isLocalCLI(false, both, "127.0.0.1:9") {
		t.Fatal("cookie plus bearer is still web")
	}
	if isLocalCLI(false, cli, "10.0.0.8:9") {
		t.Fatal("non-loopback CLI is remote")
	}
	if isLocalCLI(false, http.Header{}, "127.0.0.1:9") {
		t.Fatal("no bearer is not CLI")
	}
}

func TestRejectRemoteProcess(t *testing.T) {
	cli := http.Header{}
	cli.Set("Authorization", "Bearer tok")
	if err := rejectRemoteProcess(false, cli, "127.0.0.1:9", true, "create"); err != nil {
		t.Fatalf("local CLI: %v", err)
	}
	if err := rejectRemoteProcess(false, http.Header{}, "127.0.0.1:9", false, "create"); err != nil {
		t.Fatalf("flag off: %v", err)
	}
	err := rejectRemoteProcess(true, cli, "127.0.0.1:9", true, "create")
	if err == nil || !strings.Contains(err.Error(), "remote process create is disabled") {
		t.Fatalf("hop: %v", err)
	}
}
