package update_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/update"
)

const githubURLErr = `Get "https://api.github.com/repos/o/r/releases/latest": connection refused`

func assertPublicErrorMessage(t *testing.T, msg, want string) {
	t.Helper()
	if msg != want {
		t.Fatalf("ErrorMessage=%q want %q", msg, want)
	}
	if strings.Contains(msg, "://") || strings.Contains(msg, "github.com") || strings.Contains(msg, "/") {
		t.Fatalf("ErrorMessage leaked url/path: %q", msg)
	}
}

func testChecksums(seed string) map[string]string {
	return map[string]string{
		"linux/amd64": seed + "-amd64",
		"linux/arm64": seed + "-arm64",
		"linux/armv7": seed + "-armv7",
	}
}

type fakeSource struct {
	calls atomic.Int32
	pin   update.Pin
	err   error
	fn    func() (update.Pin, error)
}

func (f *fakeSource) Latest(context.Context) (update.Pin, error) {
	f.calls.Add(1)
	if f.fn != nil {
		return f.fn()
	}
	if f.err != nil {
		return update.Pin{}, f.err
	}
	return f.pin, nil
}

func TestCheckLatest_CacheHitWithinTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	src := &fakeSource{pin: update.Pin{
		Repository: "owner/repo",
		Tag:        "v0.2.0",
		Checksums:  testChecksums("abc"),
	}}
	c := update.NewChecker(src, func() time.Time { return now })

	first, err := c.CheckLatest(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if first.FromCache || first.CheckError || first.Pin.Tag != "v0.2.0" {
		t.Fatalf("first=%+v", first)
	}
	if src.calls.Load() != 1 {
		t.Fatalf("calls=%d", src.calls.Load())
	}

	now = now.Add(14 * time.Minute)
	second, err := c.CheckLatest(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !second.FromCache || second.Pin.Tag != "v0.2.0" || second.CheckError {
		t.Fatalf("second=%+v", second)
	}
	if src.calls.Load() != 1 {
		t.Fatalf("cache miss calls=%d", src.calls.Load())
	}
	if second.CheckedUnixMs != first.CheckedUnixMs {
		t.Fatalf("checked_at changed on cache hit: %d vs %d", second.CheckedUnixMs, first.CheckedUnixMs)
	}
}

func TestCheckLatest_RefreshBypassesCache(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	src := &fakeSource{pin: update.Pin{Repository: "o/r", Tag: "v0.1.0", Checksums: testChecksums("a")}}
	c := update.NewChecker(src, func() time.Time { return now })
	if _, err := c.CheckLatest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	src.pin = update.Pin{Repository: "o/r", Tag: "v0.2.0", Checksums: testChecksums("b")}
	got, err := c.CheckLatest(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got.FromCache || got.Pin.Tag != "v0.2.0" || src.calls.Load() != 2 {
		t.Fatalf("got=%+v calls=%d", got, src.calls.Load())
	}
}

func TestCheckLatest_SourceErrorNoCache(t *testing.T) {
	src := &fakeSource{err: errors.New(githubURLErr)}
	c := update.NewChecker(src, time.Now)
	got, err := c.CheckLatest(context.Background(), false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("err=%v", err)
	}
	if !got.CheckError {
		t.Fatalf("got=%+v", got)
	}
	assertPublicErrorMessage(t, got.ErrorMessage, "update source failed")
	if strings.Contains(err.Error(), "://") || strings.Contains(err.Error(), "github.com") {
		t.Fatalf("returned error leaked url: %v", err)
	}
}

func TestCheckLatest_SourceErrorUsesFreshCache(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	src := &fakeSource{pin: update.Pin{Repository: "o/r", Tag: "v0.3.0", Checksums: testChecksums("x")}}
	c := update.NewChecker(src, func() time.Time { return now })
	if _, err := c.CheckLatest(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	src.err = errors.New(githubURLErr)
	src.pin = update.Pin{}
	now = now.Add(time.Minute)
	got, err := c.CheckLatest(context.Background(), true)
	if err != nil {
		t.Fatalf("fresh cache should be returned: %v", err)
	}
	if !got.FromCache || !got.CheckError || got.Pin.Tag != "v0.3.0" {
		t.Fatalf("got=%+v", got)
	}
	assertPublicErrorMessage(t, got.ErrorMessage, "update source failed")
	if age := now.Sub(time.UnixMilli(got.CheckedUnixMs)); age < time.Minute || age > time.Minute+time.Second {
		t.Fatalf("age=%v checked=%d", age, got.CheckedUnixMs)
	}
}

func TestCheckLatest_IgnoresPrerelease(t *testing.T) {
	src := &fakeSource{pin: update.Pin{Repository: "o/r", Tag: "v0.4.0-rc.1", Checksums: testChecksums("x")}}
	c := update.NewChecker(src, time.Now)
	got, err := c.CheckLatest(context.Background(), false)
	if err == nil {
		t.Fatal("expected prerelease rejection")
	}
	if !got.CheckError {
		t.Fatalf("got=%+v", got)
	}
}

func TestIsBehind_Semver(t *testing.T) {
	if !update.IsBehind("0.1.0", "v0.2.0") {
		t.Fatal("0.1.0 should be behind v0.2.0")
	}
	if update.IsBehind("v0.1.0", "0.1.0") {
		t.Fatal("equal after stripping v")
	}
	if update.IsBehind("0.2.0", "0.1.0") {
		t.Fatal("current newer is not behind")
	}
	if update.IsBehind("v0.2.0", "v0.2.0") {
		t.Fatal("equal not behind")
	}
}

func TestIsBehind_NonSemver(t *testing.T) {
	// 0.2.0-dev is valid semver (prerelease). Patch base 0.2.0 > 0.1.0 → not behind.
	if update.IsBehind("0.2.0-dev", "0.1.0") {
		t.Fatal("0.2.0-dev should not be behind 0.1.0")
	}
	if !update.IsBehind("garbage", "0.1.0") {
		t.Fatal("non-semver different string is behind/different")
	}
	if update.IsBehind("garbage", "garbage") {
		t.Fatal("identical non-semver is not behind")
	}
	if !update.IsBehind("0.1.0", "not-a-version") {
		t.Fatal("semver current vs non-semver latest: different → behind")
	}
}

func TestParseChecksums_LinuxArches(t *testing.T) {
	body := "" +
		"aaa111  procmesh_0.2.0_linux_amd64.tar.gz\n" +
		"bbb222  procmesh_0.2.0_linux_arm64.tar.gz\n" +
		"ccc333  procmesh_0.2.0_darwin_amd64.tar.gz\n" +
		"ddd444  procmesh_0.2.0_linux_armv7.tar.gz\n" +
		"eee555  procmesh_0.1.0_linux_amd64.tar.gz\n"
	got, err := update.ParseChecksums(body, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"linux/amd64": "aaa111",
		"linux/arm64": "bbb222",
		"linux/armv7": "ddd444",
	}
	if len(got) != len(want) {
		t.Fatalf("got=%v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("got[%s]=%q want %q full=%v", k, got[k], v, got)
		}
	}
}

func TestParseChecksums_MissingArchInvalid(t *testing.T) {
	body := "aaa111  procmesh_0.2.0_linux_amd64.tar.gz\n"
	_, err := update.ParseChecksums(body, "0.2.0")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err=%v", err)
	}
}

func TestAnyLiveLinuxBehind(t *testing.T) {
	now := time.UnixMilli(1_700_000_010_000)
	live := now.UnixMilli()
	stale := now.Add(-time.Minute).UnixMilli()
	members := []cluster.NodeSummary{
		{NodeID: "a", State: cluster.StateAlive, OS: "linux", AgentVersion: "0.1.0", LastUpdatedUnixMs: live},
		{NodeID: "b", State: cluster.StateAlive, OS: "darwin", AgentVersion: "0.1.0", LastUpdatedUnixMs: live},
		{NodeID: "c", State: cluster.StateFailed, OS: "linux", AgentVersion: "0.1.0", LastUpdatedUnixMs: live},
		{NodeID: "d", State: cluster.StateAlive, OS: "", AgentVersion: "0.1.0", LastUpdatedUnixMs: live},
		{NodeID: "e", State: cluster.StateAlive, OS: "linux", AgentVersion: "0.2.0", LastUpdatedUnixMs: live},
		{NodeID: "stale", State: cluster.StateAlive, OS: "linux", AgentVersion: "0.1.0", LastUpdatedUnixMs: stale},
	}
	if !update.AnyLiveLinuxBehind(members, "v0.2.0", now) {
		t.Fatal("expected LIVE linux a behind")
	}
	if update.AnyLiveLinuxBehind(members, "v0.1.0", now) {
		t.Fatal("nobody behind 0.1.0")
	}
	onlyMac := []cluster.NodeSummary{
		{NodeID: "b", State: cluster.StateAlive, OS: "darwin", AgentVersion: "0.1.0", LastUpdatedUnixMs: live},
		{NodeID: "d", State: cluster.StateAlive, OS: "", AgentVersion: "0.1.0", LastUpdatedUnixMs: live},
	}
	if update.AnyLiveLinuxBehind(onlyMac, "v0.9.0", now) {
		t.Fatal("empty OS is not linux; mac ignored")
	}
	onlyStale := []cluster.NodeSummary{
		{NodeID: "stale", State: cluster.StateAlive, OS: "linux", AgentVersion: "0.1.0", LastUpdatedUnixMs: stale},
	}
	if update.AnyLiveLinuxBehind(onlyStale, "v0.9.0", now) {
		t.Fatal("STALE ALIVE linux must not count")
	}
	unknown := []cluster.NodeSummary{
		{NodeID: "u", State: cluster.StateAlive, OS: "linux", AgentVersion: "0.1.0"},
	}
	if update.AnyLiveLinuxBehind(unknown, "v0.9.0", now) {
		t.Fatal("UNKNOWN linux must not count")
	}
}
