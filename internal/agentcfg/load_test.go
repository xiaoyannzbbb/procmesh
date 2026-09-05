package agentcfg_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/agentcfg"
	"github.com/qleelulu/procmesh/internal/logmgr"
)

func TestLoad_MissingOptionalUsesDefaults(t *testing.T) {
	p, err := agentcfg.Load(filepath.Join(t.TempDir(), "nope.yaml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if p != logmgr.DefaultPolicy() {
		t.Fatalf("%+v", p)
	}
}

func TestLoad_MissingRequiredErrors(t *testing.T) {
	_, err := agentcfg.Load(filepath.Join(t.TempDir(), "nope.yaml"), true)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_PartialDiskKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte("disk:\n  auto_delete: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := agentcfg.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !p.AutoDelete || p.WarnPercent != 85 || !p.EmergencyStopWrites {
		t.Fatalf("%+v", p)
	}
}

func TestLoad_InvalidOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	body := "disk:\n  warn_percent: 90\n  cleanup_percent: 80\n  emergency_percent: 95\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Load(path, true); err == nil {
		t.Fatal("expected validate error")
	}
}

func TestLoadAll_DataDirAndListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	body := "data_dir: /var/lib/procmesh\nlisten: 0.0.0.0:18680\nadvertise: 10.0.0.1\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/var/lib/procmesh" || cfg.Listen != "0.0.0.0:18680" || cfg.Advertise != "10.0.0.1" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestLoadAll_PprofListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	body := "pprof:\n  listen: 127.0.0.1:6060\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pprof.Listen != "127.0.0.1:6060" {
		t.Fatalf("pprof listen = %q", cfg.Pprof.Listen)
	}
}

func TestLoadAll_GossipListenAndAdvertise(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	body := "gossip:\n  listen: 127.0.0.1:18689\n  advertise: 10.0.0.1:18689\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gossip.Listen != "127.0.0.1:18689" || cfg.Gossip.Advertise != "10.0.0.1:18689" {
		t.Fatalf("%+v", cfg.Gossip)
	}
	if cfg.Disk != logmgr.DefaultPolicy() {
		t.Fatalf("disk %+v", cfg.Disk)
	}
}

func TestLoadAll_GossipCompression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	body := "gossip:\n  compression: true\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Gossip.Compression {
		t.Fatal("gossip compression override was not loaded")
	}
}

func TestLoadAll_RPCListenAndAdvertise(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	body := "rpc:\n  listen: 127.0.0.1:18683\n  advertise: 10.0.0.1:18683\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RPC.Listen != "127.0.0.1:18683" || cfg.RPC.Advertise != "10.0.0.1:18683" {
		t.Fatalf("%+v", cfg.RPC)
	}
}

func TestLoadAll_ControlListenAndAdvertise(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	body := "control:\n  listen: 127.0.0.1:18685\n  advertise: 10.0.0.1:18685\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Control.Listen != "127.0.0.1:18685" || cfg.Control.Advertise != "10.0.0.1:18685" {
		t.Fatalf("%+v", cfg.Control)
	}
}

func TestLoadAll_BreakGlassSocketAndGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	body := "break_glass:\n  socket: /run/procmesh/break-glass.sock\n  group: procmesh-operators\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BreakGlass.Socket != "/run/procmesh/break-glass.sock" || cfg.BreakGlass.Group != "procmesh-operators" {
		t.Fatalf("%+v", cfg.BreakGlass)
	}
}

func TestLoad_UsesLoadAllDiskOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	body := "disk:\n  auto_delete: true\ngossip:\n  listen: 127.0.0.1:0\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := agentcfg.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !p.AutoDelete {
		t.Fatalf("%+v", p)
	}
}

func TestDefaultPath_DarwinOrLinux(t *testing.T) {
	p := agentcfg.DefaultPath()
	if runtime.GOOS == "darwin" {
		if !strings.HasSuffix(p, "/.config/procmesh/agent.yaml") {
			t.Fatalf("%s", p)
		}
	} else if p != "/etc/procmesh/agent.yaml" {
		t.Fatalf("%s", p)
	}
}

func TestLoadAll_BatchDefaultsAndOverride(t *testing.T) {
	cfg, err := agentcfg.LoadAll(filepath.Join(t.TempDir(), "nope.yaml"), false)
	if err != nil || cfg.Batch.MaxConcurrency != 16 || cfg.Batch.TargetTimeout != 30*time.Second {
		t.Fatalf("%+v %v", cfg.Batch, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte("batch:\n  max_concurrency: 4\n  target_timeout: 2s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = agentcfg.LoadAll(path, true)
	if err != nil || cfg.Batch.MaxConcurrency != 4 || cfg.Batch.TargetTimeout != 2*time.Second {
		t.Fatalf("%+v %v", cfg.Batch, err)
	}
}

func TestLoadAll_BatchRejectsOutOfRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("batch:\n  max_concurrency: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.LoadAll(path, true); err == nil {
		t.Fatal("expected INVALID")
	}
}

func TestLoadAll_BackupS3AndDefaultSchedule(t *testing.T) {
	cfg, err := agentcfg.LoadAll(filepath.Join(t.TempDir(), "nope.yaml"), false)
	if err != nil || cfg.Backup.Schedule != "" || cfg.Backup.S3.Bucket != "" {
		t.Fatalf("default backup %+v %v", cfg.Backup, err)
	}

	path := filepath.Join(t.TempDir(), "agent.yaml")
	body := "backup:\n  s3:\n    bucket: snaps\n    endpoint: https://s3.example.com\n    access_key: yaml-ak\n    secret_key: yaml-sk\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = agentcfg.LoadAll(path, true)
	if err != nil || cfg.Backup.S3.Bucket != "snaps" || cfg.Backup.Schedule != "" {
		t.Fatalf("parsed %+v %v", cfg.Backup, err)
	}
	if cfg.Backup.S3.AccessKey != "yaml-ak" || cfg.Backup.S3.SecretKey != "yaml-sk" {
		t.Fatalf("yaml keys %+v", cfg.Backup.S3)
	}

	t.Setenv("PROCMESH_S3_ACCESS_KEY", "env-ak")
	t.Setenv("PROCMESH_S3_SECRET_KEY", "env-sk")
	cfg, err = agentcfg.LoadAll(path, true)
	if err != nil || cfg.Backup.S3.AccessKey != "env-ak" || cfg.Backup.S3.SecretKey != "env-sk" {
		t.Fatalf("env override %+v %v", cfg.Backup.S3, err)
	}
}

func TestLoadAll_BackupNamedS3Profiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	body := `backup:
  s3_profiles:
    archive:
      endpoint: https://archive.example.com
      bucket: archive-snaps
      prefix: procmesh
      region: eu-west-1
      access_key: archive-ak
      secret_key: archive-sk
    local-test:
      endpoint: http://127.0.0.1:18680
      bucket: local-snaps
      insecure: true
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	archive, ok := cfg.Backup.S3Profiles["archive"]
	if !ok {
		t.Fatalf("archive profile missing: %+v", cfg.Backup.S3Profiles)
	}
	if archive.Endpoint != "https://archive.example.com" || archive.Bucket != "archive-snaps" || archive.Prefix != "procmesh" || archive.Region != "eu-west-1" {
		t.Fatalf("archive profile = %+v", archive)
	}
	if archive.AccessKey != "archive-ak" || archive.SecretKey != "archive-sk" {
		t.Fatalf("archive credentials not parsed: %+v", archive)
	}
	if local := cfg.Backup.S3Profiles["local-test"]; !local.Insecure || local.Bucket != "local-snaps" {
		t.Fatalf("local profile = %+v", local)
	}
}

func TestLoadAll_ProcessRemoteDefaultsAllow(t *testing.T) {
	cfg, err := agentcfg.LoadAll(filepath.Join(t.TempDir(), "nope.yaml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Process.DisableRemoteCreate || cfg.Process.DisableRemoteUpdate || cfg.Process.DisableRemoteDelete {
		t.Fatalf("defaults must allow remote: %+v", cfg.Process)
	}

	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("listen: 127.0.0.1:18680\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Process.DisableRemoteCreate || cfg.Process.DisableRemoteUpdate || cfg.Process.DisableRemoteDelete {
		t.Fatalf("omitted process section must allow remote: %+v", cfg.Process)
	}
}

func TestLoadAll_UpdateDefaults(t *testing.T) {
	cfg, err := agentcfg.LoadAll(filepath.Join(t.TempDir(), "nope.yaml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Update.Repository != agentcfg.DefaultUpdateRepository || !cfg.Update.Enabled {
		t.Fatalf("missing file defaults %+v", cfg.Update)
	}

	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("listen: 127.0.0.1:18680\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Update.Repository != agentcfg.DefaultUpdateRepository || !cfg.Update.Enabled {
		t.Fatalf("omitted update section %+v", cfg.Update)
	}
}

func TestLoadAll_UpdateOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	body := "update:\n  repository: fork/procmesh\n  enabled: false\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Update.Repository != "fork/procmesh" || cfg.Update.Enabled {
		t.Fatalf("override %+v", cfg.Update)
	}
}

func TestLoadAll_UpdatePartialKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("update:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Update.Repository != agentcfg.DefaultUpdateRepository || cfg.Update.Enabled {
		t.Fatalf("partial %+v", cfg.Update)
	}
}

func TestLoadAll_UpdateRejectsInvalidRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("update:\n  repository: not a repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.LoadAll(path, true); err == nil {
		t.Fatal("expected INVALID repository")
	}
}

func TestLoadAll_ProcessRemoteDisableFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	body := "process:\n  disable_remote_create: true\n  disable_remote_update: true\n  disable_remote_delete: true\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentcfg.LoadAll(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Process.DisableRemoteCreate || !cfg.Process.DisableRemoteUpdate || !cfg.Process.DisableRemoteDelete {
		t.Fatalf("explicit disable flags: %+v", cfg.Process)
	}
}
