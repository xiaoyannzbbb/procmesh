package control_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestCreateAndConsumeToken_Once(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	plain, info, err := control.CreateToken(dir, 0, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "pmj_") || info.Remaining != 1 {
		t.Fatalf("plain=%q info=%+v", plain, info)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "tokens.json"))
	if strings.Contains(string(raw), plain) {
		t.Fatal("plaintext leaked into tokens.json")
	}
	if err := control.ConsumeToken(dir, plain, now); err != nil {
		t.Fatal(err)
	}
	err = control.ConsumeToken(dir, plain, now)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("second consume: %v", err)
	}
}

func TestConsumeToken_Expired(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	plain, _, err := control.CreateToken(dir, time.Second, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	err = control.ConsumeToken(dir, plain, now.Add(2*time.Second))
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("got %v", err)
	}
}

func TestConsumeToken_Invalid(t *testing.T) {
	err := control.ConsumeToken(t.TempDir(), "pmj_nope", time.Now())
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestRevokeToken(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	plain, info, err := control.CreateToken(dir, time.Hour, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.RevokeToken(dir, info.ID); err != nil {
		t.Fatal(err)
	}
	err = control.ConsumeToken(dir, plain, now)
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("got %v", err)
	}
}

func TestCreateToken_MultiUse(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	plain, info, err := control.CreateToken(dir, time.Hour, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if info.Remaining != 2 {
		t.Fatalf("%+v", info)
	}
	if err := control.ConsumeToken(dir, plain, now); err != nil {
		t.Fatal(err)
	}
	if err := control.ConsumeToken(dir, plain, now); err != nil {
		t.Fatal(err)
	}
	if err := control.ConsumeToken(dir, plain, now); !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("got %v", err)
	}
}
