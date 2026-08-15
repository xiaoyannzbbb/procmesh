package rpc_test

import (
	"net/http"
	"testing"

	"github.com/qleelulu/procmesh/internal/rpc"
)

func TestHeader_TargetAndSource(t *testing.T) {
	h := make(http.Header)
	rpc.SetTarget(h, "node-c")
	rpc.SetSource(h, "node-a")
	if rpc.TargetOf(h) != "node-c" || rpc.SourceOf(h) != "node-a" {
		t.Fatalf("%v", h)
	}
}

func TestHeader_UserSessionToken(t *testing.T) {
	h := make(http.Header)
	rpc.SetUserID(h, "u1")
	rpc.SetSessionID(h, "s1")
	rpc.SetTokenID(h, "t1")
	if rpc.UserIDOf(h) != "u1" || rpc.SessionIDOf(h) != "s1" || rpc.TokenIDOf(h) != "t1" {
		t.Fatalf("%v", h)
	}
	rpc.SetUserID(h, "")
	rpc.SetSessionID(h, "")
	rpc.SetTokenID(h, "")
	if rpc.UserIDOf(h) != "u1" || rpc.SessionIDOf(h) != "s1" || rpc.TokenIDOf(h) != "t1" {
		t.Fatalf("empty set must be no-op: %v", h)
	}
}

func TestHeader_CopyIdentity(t *testing.T) {
	dst := make(http.Header)
	rpc.SetUserID(dst, "old")
	rpc.SetSessionID(dst, "olds")
	rpc.SetTokenID(dst, "oldt")
	src := make(http.Header)
	rpc.SetUserID(src, "u1")
	rpc.SetSessionID(src, "s1")
	rpc.CopyIdentity(dst, src)
	if rpc.UserIDOf(dst) != "u1" || rpc.SessionIDOf(dst) != "s1" || rpc.TokenIDOf(dst) != "" {
		t.Fatalf("%v", dst)
	}
	rpc.CopyIdentity(dst, nil)
	if rpc.UserIDOf(dst) != "" || rpc.SessionIDOf(dst) != "" {
		t.Fatalf("nil src should clear: %v", dst)
	}
}
