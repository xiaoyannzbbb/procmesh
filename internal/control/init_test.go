package control_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestInit_WritesSecretCAAndAdmin(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	res, err := control.Init(dir, "nid-1", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if res.AdminUser != "admin" {
		t.Fatalf("user=%q", res.AdminUser)
	}
	if len(res.AdminPassword) < 10 {
		t.Fatalf("short password")
	}
	if !looksUUID(res.ClusterID) {
		t.Fatalf("cluster_id=%q", res.ClusterID)
	}

	meta, err := control.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.ControlMember || meta.NodeID != "nid-1" || meta.ClusterID != res.ClusterID {
		t.Fatalf("%+v", meta)
	}
	sec, err := os.ReadFile(filepath.Join(dir, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(sec))) != 64 {
		t.Fatalf("secret hex len=%d", len(strings.TrimSpace(string(sec))))
	}
	st, _ := os.Stat(filepath.Join(dir, "secret"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secret perm=%o", st.Mode().Perm())
	}
	st, _ = os.Stat(filepath.Join(dir, "admin.bootstrap"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("admin perm=%o", st.Mode().Perm())
	}

	b, err := control.LoadBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.VerifyAgent(b.CACertPEM, b.AgentCertPEM, res.ClusterID, "nid-1", now); err != nil {
		t.Fatal(err)
	}
	if !control.VerifyPassword(mustReadHash(t, dir), res.AdminPassword) {
		t.Fatal("password hash mismatch")
	}
}

func TestInit_ConflictIfExists(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if _, err := control.Init(dir, "n", "admin", now); err != nil {
		t.Fatal(err)
	}
	_, err := control.Init(dir, "n", "admin", now)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("got %v", err)
	}
}

func TestLoadMeta_NotFound(t *testing.T) {
	_, err := control.LoadMeta(t.TempDir())
	if !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("got %v", err)
	}
}

func TestAppendGossipSeed_Dedupe(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if _, err := control.Init(dir, "n", "admin", now); err != nil {
		t.Fatal(err)
	}
	if err := control.AppendGossipSeed(dir, ""); err != nil {
		t.Fatal(err)
	}
	meta, err := control.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.GossipSeeds) != 0 {
		t.Fatalf("empty addr wrote seeds=%v", meta.GossipSeeds)
	}
	if err := control.AppendGossipSeed(dir, "127.0.0.1:7947"); err != nil {
		t.Fatal(err)
	}
	if err := control.AppendGossipSeed(dir, "127.0.0.1:7947"); err != nil {
		t.Fatal(err)
	}
	if err := control.AppendGossipSeed(dir, "127.0.0.1:7948"); err != nil {
		t.Fatal(err)
	}
	meta, err = control.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.GossipSeeds) != 2 || meta.GossipSeeds[0] != "127.0.0.1:7947" || meta.GossipSeeds[1] != "127.0.0.1:7948" {
		t.Fatalf("seeds=%v", meta.GossipSeeds)
	}
}

func mustReadHash(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "admin.bootstrap"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		PasswordHash string `json:"password_hash"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.PasswordHash == "" {
		t.Fatal("empty password_hash")
	}
	return doc.PasswordHash
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func looksUUID(s string) bool {
	return uuidRE.MatchString(s)
}
