package update_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/update"
)

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func makeTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     0o755,
			Size:     int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func releaseFiles(version string, binaries map[string][]byte) map[string][]byte {
	prefix := "procmesh_" + version + "_linux_amd64/"
	out := make(map[string][]byte, len(binaries)+1)
	out[prefix+"README.md"] = []byte("readme")
	for name, body := range binaries {
		out[prefix+name] = body
	}
	return out
}

func linuxPin(tag, platform, sum string) update.Pin {
	return update.Pin{
		Repository: "owner/procmesh",
		Tag:        tag,
		Checksums: map[string]string{
			"linux/amd64": "amd64-placeholder",
			"linux/arm64": "arm64-placeholder",
			"linux/armv7": "armv7-placeholder",
			platform:      sum,
		},
	}
}

type fakeRestarter struct {
	calls atomic.Int32
	err   error
	ctx   context.Context
}

type captureInstaller struct {
	ctx   context.Context
	inner update.Installer
}

func (c *captureInstaller) Install(ctx context.Context, tarball []byte, destDir, previousDir string) error {
	c.ctx = ctx
	inner := c.inner
	if inner == nil {
		inner = update.TarInstaller{}
	}
	return inner.Install(ctx, tarball, destDir, previousDir)
}

func sameDeadline(a, b context.Context) bool {
	da, oka := a.Deadline()
	db, okb := b.Deadline()
	if oka != okb {
		return false
	}
	if !oka {
		return true
	}
	return da.Equal(db)
}

func (f *fakeRestarter) Restart(ctx context.Context) error {
	f.ctx = ctx
	f.calls.Add(1)
	return f.err
}

type recordingDownload struct {
	mu    sync.Mutex
	calls int
	repo  string
	tag   string
	asset string
	body  []byte
	err   error

	blockStarted chan struct{}
	blockUntil   chan struct{}
}

func (d *recordingDownload) fn(ctx context.Context, repo, tag, asset string) ([]byte, error) {
	d.mu.Lock()
	d.calls++
	d.repo = repo
	d.tag = tag
	d.asset = asset
	d.mu.Unlock()
	if d.blockStarted != nil {
		select {
		case <-d.blockStarted:
		default:
			close(d.blockStarted)
		}
	}
	if d.blockUntil != nil {
		select {
		case <-d.blockUntil:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if d.err != nil {
		return nil, d.err
	}
	return d.body, ctx.Err()
}

func (d *recordingDownload) snapshot() (calls int, repo, tag, asset string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls, d.repo, d.tag, d.asset
}

func testApplier(t *testing.T, dl *recordingDownload, rst *fakeRestarter) *update.Applier {
	t.Helper()
	dest := t.TempDir()
	data := t.TempDir()
	writeFile(t, filepath.Join(dest, "procmesh-agent"), "old-agent")
	writeFile(t, filepath.Join(dest, "procmesh-shim"), "old-shim")
	writeFile(t, filepath.Join(dest, "procmesh"), "old-cli")
	return &update.Applier{
		Enabled:   true,
		DataDir:   data,
		Version:   "0.1.0",
		GOOS:      "linux",
		GOARCH:    "amd64",
		BinDir:    dest,
		Download:  dl.fn,
		Restarter: rst,
	}
}

func TestApply_AlreadyAtPinVersionNoDownload(t *testing.T) {
	dl := &recordingDownload{body: []byte("unused")}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	a.Version = "0.2.0"
	pin := linuxPin("v0.2.0", "linux/amd64", sha256hex([]byte("unused")))

	if err := a.Apply(context.Background(), pin); err != nil {
		t.Fatal(err)
	}
	calls, _, _, _ := dl.snapshot()
	if calls != 0 {
		t.Fatalf("download calls=%d, want 0", calls)
	}
	if rst.calls.Load() != 0 {
		t.Fatalf("restart calls=%d, want 0", rst.calls.Load())
	}
	if got := readFile(t, filepath.Join(a.BinDir, "procmesh-agent")); got != "old-agent" {
		t.Fatalf("binaries replaced on no-op apply: %q", got)
	}
}

func TestApply_EnabledFalseDenied(t *testing.T) {
	dl := &recordingDownload{body: []byte("unused")}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	a.Enabled = false
	err := a.Apply(context.Background(), linuxPin("v0.2.0", "linux/amd64", sha256hex([]byte("unused"))))
	if !errcode.Is(err, errcode.DENIED) {
		t.Fatalf("err=%v", err)
	}
	calls, _, _, _ := dl.snapshot()
	if calls != 0 {
		t.Fatalf("download calls=%d", calls)
	}
}

func TestApply_NonLinuxInvalid(t *testing.T) {
	dl := &recordingDownload{body: []byte("unused")}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	a.GOOS = "darwin"
	err := a.Apply(context.Background(), linuxPin("v0.2.0", "linux/amd64", sha256hex([]byte("unused"))))
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err=%v", err)
	}
	calls, _, _, _ := dl.snapshot()
	if calls != 0 {
		t.Fatalf("download calls=%d", calls)
	}
	if rst.calls.Load() != 0 {
		t.Fatalf("restart calls=%d", rst.calls.Load())
	}
}

func TestApply_ChecksumMismatchInvalid(t *testing.T) {
	body := makeTarGz(t, releaseFiles("0.2.0", map[string][]byte{
		"procmesh-agent": []byte("new-agent"),
		"procmesh-shim":  []byte("new-shim"),
		"procmesh":       []byte("new-cli"),
	}))
	dl := &recordingDownload{body: body}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	err := a.Apply(context.Background(), linuxPin("v0.2.0", "linux/amd64", strings.Repeat("ab", 32)))
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err=%v", err)
	}
	if rst.calls.Load() != 0 {
		t.Fatalf("restart calls=%d", rst.calls.Load())
	}
	if got := readFile(t, filepath.Join(a.BinDir, "procmesh-agent")); got != "old-agent" {
		t.Fatalf("installed on checksum mismatch: %q", got)
	}
	if errorChainHasURL(err) {
		t.Fatalf("url leaked: %v", err)
	}
}

func TestApply_ConcurrentConflict(t *testing.T) {
	body := makeTarGz(t, releaseFiles("0.2.0", map[string][]byte{
		"procmesh-agent": []byte("new-agent"),
		"procmesh-shim":  []byte("new-shim"),
		"procmesh":       []byte("new-cli"),
	}))
	dl := &recordingDownload{
		body:         body,
		blockStarted: make(chan struct{}),
		blockUntil:   make(chan struct{}),
	}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	pin := linuxPin("v0.2.0", "linux/amd64", sha256hex(body))

	errCh := make(chan error, 1)
	go func() { errCh <- a.Apply(context.Background(), pin) }()
	select {
	case <-dl.blockStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first apply did not start download")
	}

	other := testApplier(t, &recordingDownload{body: body}, &fakeRestarter{})
	other.Download = dl.fn
	err := other.Apply(context.Background(), pin)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("err=%v", err)
	}

	close(dl.blockUntil)
	if err := <-errCh; err != nil {
		t.Fatalf("first apply: %v", err)
	}
}

func TestApply_PathTraversalArchiveInvalid(t *testing.T) {
	body := makeTarGz(t, map[string][]byte{
		"../procmesh-agent": []byte("new-agent"),
		"procmesh-shim":     []byte("new-shim"),
		"procmesh":          []byte("new-cli"),
	})
	dl := &recordingDownload{body: body}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	err := a.Apply(context.Background(), linuxPin("v0.2.0", "linux/amd64", sha256hex(body)))
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err=%v", err)
	}
	if rst.calls.Load() != 0 {
		t.Fatalf("restart calls=%d", rst.calls.Load())
	}
	if got := readFile(t, filepath.Join(a.BinDir, "procmesh-agent")); got != "old-agent" {
		t.Fatalf("replaced on traversal: %q", got)
	}
}

func TestApply_HappyPathDownloadInstallPreviousRestart(t *testing.T) {
	body := makeTarGz(t, releaseFiles("0.2.0", map[string][]byte{
		"procmesh-agent": []byte("new-agent"),
		"procmesh-shim":  []byte("new-shim"),
		"procmesh":       []byte("new-cli"),
	}))
	dl := &recordingDownload{body: body}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	pin := linuxPin("v0.2.0", "linux/amd64", sha256hex(body))

	if err := a.Apply(context.Background(), pin); err != nil {
		t.Fatal(err)
	}
	calls, repo, tag, asset := dl.snapshot()
	if calls != 1 || repo != "owner/procmesh" || tag != "v0.2.0" {
		t.Fatalf("download repo=%s tag=%s calls=%d", repo, tag, calls)
	}
	if asset != "procmesh_0.2.0_linux_amd64.tar.gz" {
		t.Fatalf("asset=%q", asset)
	}
	if strings.Contains(strings.ToLower(asset), "latest") {
		t.Fatalf("must not use latest: %q", asset)
	}
	if rst.calls.Load() != 1 {
		t.Fatalf("restart calls=%d", rst.calls.Load())
	}
	if got := readFile(t, filepath.Join(a.BinDir, "procmesh-agent")); got != "new-agent" {
		t.Fatalf("agent=%q", got)
	}
	if got := readFile(t, filepath.Join(a.BinDir, "procmesh-shim")); got != "new-shim" {
		t.Fatalf("shim=%q", got)
	}
	if got := readFile(t, filepath.Join(a.BinDir, "procmesh")); got != "new-cli" {
		t.Fatalf("cli=%q", got)
	}
	prev := filepath.Join(a.DataDir, "update", "previous")
	if got := readFile(t, filepath.Join(prev, "procmesh-agent")); got != "old-agent" {
		t.Fatalf("previous agent=%q", got)
	}
	if got := readFile(t, filepath.Join(prev, "procmesh-shim")); got != "old-shim" {
		t.Fatalf("previous shim=%q", got)
	}
	if got := readFile(t, filepath.Join(prev, "procmesh")); got != "old-cli" {
		t.Fatalf("previous cli=%q", got)
	}
}

func TestApply_ArmGOARCHUsesArmv7Pin(t *testing.T) {
	body := makeTarGz(t, map[string][]byte{
		"procmesh-agent": []byte("new-agent"),
		"procmesh-shim":  []byte("new-shim"),
		"procmesh":       []byte("new-cli"),
	})
	dl := &recordingDownload{body: body}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	a.GOARCH = "arm"
	pin := linuxPin("v0.2.0", "linux/armv7", sha256hex(body))

	if err := a.Apply(context.Background(), pin); err != nil {
		t.Fatal(err)
	}
	_, _, _, asset := dl.snapshot()
	if asset != "procmesh_0.2.0_linux_armv7.tar.gz" {
		t.Fatalf("asset=%q", asset)
	}
}

func TestApply_DownloadTimeout(t *testing.T) {
	dl := &recordingDownload{
		blockStarted: make(chan struct{}),
		blockUntil:   make(chan struct{}),
	}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	defer close(dl.blockUntil)

	err := a.Apply(ctx, linuxPin("v0.2.0", "linux/amd64", strings.Repeat("cd", 32)))
	if !errcode.Is(err, errcode.TIMEOUT) {
		t.Fatalf("err=%v", err)
	}
	if rst.calls.Load() != 0 {
		t.Fatalf("restart calls=%d", rst.calls.Load())
	}
	if errorChainHasURL(err) {
		t.Fatalf("url leaked: %v", err)
	}
}

func TestDownloadTimeoutConstant(t *testing.T) {
	if update.DownloadTimeout != 5*time.Minute {
		t.Fatalf("DownloadTimeout=%s", update.DownloadTimeout)
	}
}

func TestApply_TimeoutCoversDownloadAndVerifyOnly(t *testing.T) {
	body := makeTarGz(t, releaseFiles("0.2.0", map[string][]byte{
		"procmesh-agent": []byte("new-agent"),
		"procmesh-shim":  []byte("new-shim"),
		"procmesh":       []byte("new-cli"),
	}))
	dl := &recordingDownload{body: body}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	inst := &captureInstaller{}
	a.Installer = inst
	var downloadCtx context.Context
	orig := a.Download
	a.Download = func(ctx context.Context, repo, tag, asset string) ([]byte, error) {
		downloadCtx = ctx
		return orig(ctx, repo, tag, asset)
	}
	parent := context.Background()
	pin := linuxPin("v0.2.0", "linux/amd64", sha256hex(body))

	if err := a.Apply(parent, pin); err != nil {
		t.Fatal(err)
	}
	if downloadCtx == nil {
		t.Fatal("download was not called")
	}
	if _, ok := downloadCtx.Deadline(); !ok {
		t.Fatal("download+verify ctx must have the 5-minute budget")
	}
	if sameDeadline(downloadCtx, parent) {
		t.Fatal("download+verify must use a child timeout, not the parent ctx")
	}
	if inst.ctx == nil {
		t.Fatal("install was not called")
	}
	if !sameDeadline(inst.ctx, parent) {
		t.Fatal("install must use the parent ctx, not the download timeout")
	}
	if rst.ctx == nil {
		t.Fatal("restart was not called")
	}
	if !sameDeadline(rst.ctx, parent) {
		t.Fatal("restart must use the parent ctx, not the download timeout")
	}
}

func TestApply_CanceledIsUnavailableNotTimeout(t *testing.T) {
	dl := &recordingDownload{
		blockStarted: make(chan struct{}),
		blockUntil:   make(chan struct{}),
	}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(dl.blockUntil)

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.Apply(ctx, linuxPin("v0.2.0", "linux/amd64", strings.Repeat("cd", 32)))
	}()
	select {
	case <-dl.blockStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	cancel()
	var err error
	select {
	case err = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("apply did not return after cancel")
	}
	if errcode.Is(err, errcode.TIMEOUT) {
		t.Fatalf("canceled must not be TIMEOUT: %v", err)
	}
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("canceled want UNAVAILABLE, got %v", err)
	}
	if rst.calls.Load() != 0 {
		t.Fatalf("restart calls=%d", rst.calls.Load())
	}
	if errorChainHasURL(err) {
		t.Fatalf("url leaked: %v", err)
	}
}

func TestApply_DownloadDoErrorHasNoURL(t *testing.T) {
	dl := &recordingDownload{err: errors.New(`Get "http://github.com/owner/procmesh/releases/download/v0.2.0/procmesh_0.2.0_linux_amd64.tar.gz": EOF`)}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	err := a.Apply(context.Background(), linuxPin("v0.2.0", "linux/amd64", strings.Repeat("cd", 32)))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("err=%v", err)
	}
	if errorChainHasURL(err) {
		t.Fatalf("url leaked: %v", err)
	}
	if strings.Contains(err.Error(), "http://") {
		t.Fatalf("url leaked in Error: %v", err)
	}
}

func TestAgentRestarter_PrefersSystemctl(t *testing.T) {
	var ran []string
	var execCalls int
	r := update.AgentRestarter{
		UnitAvailable: func() bool { return true },
		RunSystemctl: func(_ context.Context, args ...string) error {
			ran = append(ran, args...)
			return nil
		},
		AgentPath: "/tmp/procmesh-agent",
		Exec: func(string, []string, []string) error {
			execCalls++
			return nil
		},
	}
	if err := r.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 2 || ran[0] != "restart" || ran[1] != "procmesh-agent" {
		t.Fatalf("systemctl args=%v", ran)
	}
	if execCalls != 0 {
		t.Fatalf("self-exec called")
	}
}

func TestAgentRestarter_SelfExecWhenNoSystemd(t *testing.T) {
	var gotArgv0 string
	var gotArgv, gotEnv []string
	r := update.AgentRestarter{
		UnitAvailable: func() bool { return false },
		AgentPath:     "/opt/procmesh/procmesh-agent",
		Args:          []string{"procmesh-agent", "--data-dir", "/var/lib/procmesh"},
		Env:           []string{"A=1"},
		Exec: func(argv0 string, argv, envv []string) error {
			gotArgv0 = argv0
			gotArgv = append([]string(nil), argv...)
			gotEnv = append([]string(nil), envv...)
			return nil
		},
	}
	if err := r.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotArgv0 != "/opt/procmesh/procmesh-agent" {
		t.Fatalf("argv0=%s", gotArgv0)
	}
	if len(gotArgv) != 3 || gotArgv[0] != "procmesh-agent" {
		t.Fatalf("argv=%v", gotArgv)
	}
	if len(gotEnv) != 1 || gotEnv[0] != "A=1" {
		t.Fatalf("env=%v", gotEnv)
	}
}

func TestAgentRestarter_NeitherAvailable(t *testing.T) {
	r := update.AgentRestarter{
		UnitAvailable: func() bool { return false },
	}
	err := r.Restart(context.Background())
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("err=%v", err)
	}
}
