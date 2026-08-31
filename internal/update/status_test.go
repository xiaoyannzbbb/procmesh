package update_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/update"
)

func TestSkipNotLive_PrefersStateThenFreshness(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state, classified, want string
	}{
		{"FAILED", freshness.STALE, update.SkipFAILED},
		{"FAILED", freshness.UNKNOWN, update.SkipFAILED},
		{"SUSPECT", freshness.STALE, update.SkipSUSPECT},
		{"ALIVE", freshness.STALE, update.SkipSTALE},
		{"ALIVE", freshness.UNKNOWN, update.SkipUNKNOWN},
		{"LEFT", freshness.STALE, update.SkipSTALE},
	}
	for _, tc := range cases {
		if got := update.SkipNotLive(tc.state, tc.classified); got != tc.want {
			t.Errorf("state=%s classified=%s got=%s want=%s", tc.state, tc.classified, got, tc.want)
		}
	}
}

func TestEvaluateProbed_SkipReasons(t *testing.T) {
	t.Parallel()
	latest := "v0.2.0"
	cases := []struct {
		name     string
		in       update.ProbedInput
		eligible bool
		reason   string
		busy     bool
		os       string
	}{
		{
			name: "probe timeout is timeout not unsupported",
			in: update.ProbedInput{
				GossipOS: "linux", GossipArch: "amd64", GossipVersion: "0.1.0",
				LatestTag: latest, ProbeErr: context.DeadlineExceeded,
			},
			reason: update.SkipTIMEOUT,
			os:     "linux",
		},
		{
			name: "probe unavailable is unavailable not unsupported",
			in: update.ProbedInput{
				GossipOS: "linux", GossipArch: "amd64", GossipVersion: "0.1.0",
				LatestTag: latest, ProbeErr: errcode.E(errcode.UNAVAILABLE, "owner unreachable"),
			},
			reason: update.SkipUNAVAILABLE,
			os:     "linux",
		},
		{
			name: "probe unimplemented is unsupported",
			in: update.ProbedInput{
				GossipOS: "linux", GossipArch: "amd64", GossipVersion: "0.1.0",
				LatestTag: latest,
				ProbeErr:  connect.NewError(connect.CodeUnimplemented, errors.New("GetLocalUpdateInfo is not implemented")),
			},
			reason: update.SkipUNSUPPORTED,
			os:     "linux",
		},
		{
			name: "probe unknown method is unsupported",
			in: update.ProbedInput{
				GossipOS: "linux", GossipArch: "amd64", GossipVersion: "0.1.0",
				LatestTag: latest,
				ProbeErr:  connect.NewError(connect.CodeUnknown, errors.New("unknown method GetLocalUpdateInfo")),
			},
			reason: update.SkipUNSUPPORTED,
			os:     "linux",
		},
		{
			name: "empty latest tag is not eligible",
			in: update.ProbedInput{
				GossipOS: "linux", GossipArch: "amd64", GossipVersion: "0.1.0", LatestTag: "",
				Info: &update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true},
			},
			reason: update.SkipCHECK_FAILED,
			os:     "linux",
		},
		{
			name: "check failed is not eligible even with a tag",
			in: update.ProbedInput{
				GossipOS: "linux", GossipArch: "amd64", GossipVersion: "0.1.0",
				LatestTag: latest, CheckFailed: true,
				Info: &update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true},
			},
			reason: update.SkipCHECK_FAILED,
			os:     "linux",
		},
		{
			name: "connect deadline exceeded is timeout",
			in: update.ProbedInput{
				GossipOS: "linux", GossipArch: "amd64", GossipVersion: "0.1.0",
				LatestTag: latest,
				ProbeErr:  connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded),
			},
			reason: update.SkipTIMEOUT,
			os:     "linux",
		},
		{
			name: "connect unavailable is unavailable",
			in: update.ProbedInput{
				GossipOS: "linux", GossipArch: "amd64", GossipVersion: "0.1.0",
				LatestTag: latest,
				ProbeErr:  connect.NewError(connect.CodeUnavailable, errors.New("owner unreachable")),
			},
			reason: update.SkipUNAVAILABLE,
			os:     "linux",
		},
		{
			name: "darwin from probe is macos",
			in: update.ProbedInput{
				GossipOS: "linux", GossipVersion: "0.1.0", LatestTag: latest,
				Info: &update.LocalInfo{OS: "darwin", Arch: "arm64", Version: "0.1.0", Enabled: true},
			},
			reason: update.SkipMACOS,
			os:     "darwin",
		},
		{
			name: "darwin from gossip when probe omits os",
			in: update.ProbedInput{
				GossipOS: "darwin", GossipArch: "arm64", GossipVersion: "0.1.0", LatestTag: latest,
				Info: &update.LocalInfo{OS: "", Arch: "", Version: "0.1.0", Enabled: true},
			},
			reason: update.SkipMACOS,
			os:     "darwin",
		},
		{
			name: "empty os is not macos and can be eligible",
			in: update.ProbedInput{
				GossipOS: "", GossipArch: "", GossipVersion: "0.1.0", LatestTag: latest,
				Info: &update.LocalInfo{OS: "", Arch: "", Version: "0.1.0", Enabled: true},
			},
			eligible: true,
		},
		{
			name: "empty gossip os with empty probe os is not macos",
			in: update.ProbedInput{
				GossipOS: "", GossipVersion: "0.1.0", LatestTag: latest,
				Info: &update.LocalInfo{Enabled: true, Version: "0.1.0"},
			},
			eligible: true,
		},
		{
			name: "disabled",
			in: update.ProbedInput{
				GossipOS: "linux", GossipVersion: "0.1.0", LatestTag: latest,
				Info: &update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: false},
			},
			reason: update.SkipDISABLED,
			os:     "linux",
		},
		{
			name: "busy",
			in: update.ProbedInput{
				GossipOS: "linux", GossipVersion: "0.1.0", LatestTag: latest,
				Info: &update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true, Busy: true},
			},
			reason: update.SkipBUSY,
			busy:   true,
			os:     "linux",
		},
		{
			name: "already at pin",
			in: update.ProbedInput{
				GossipOS: "linux", GossipVersion: "0.2.0", LatestTag: latest,
				Info: &update.LocalInfo{OS: "linux", Arch: "amd64", Version: "v0.2.0", Enabled: true},
			},
			reason: update.SkipCURRENT,
			os:     "linux",
		},
		{
			name: "eligible linux behind",
			in: update.ProbedInput{
				GossipOS: "linux", GossipArch: "amd64", GossipVersion: "0.1.0", LatestTag: latest,
				Info: &update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.1.0", Enabled: true},
			},
			eligible: true,
			os:       "linux",
		},
		{
			name: "probe version wins for current check",
			in: update.ProbedInput{
				GossipOS: "linux", GossipVersion: "0.1.0", LatestTag: latest,
				Info: &update.LocalInfo{OS: "linux", Arch: "amd64", Version: "0.2.0", Enabled: true},
			},
			reason: update.SkipCURRENT,
			os:     "linux",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := update.EvaluateProbed(tc.in)
			if got.Eligible != tc.eligible || got.SkipReason != tc.reason || got.Busy != tc.busy {
				t.Fatalf("eligible=%v reason=%q busy=%v want eligible=%v reason=%q busy=%v",
					got.Eligible, got.SkipReason, got.Busy, tc.eligible, tc.reason, tc.busy)
			}
			if tc.os != "" && got.OS != tc.os {
				t.Fatalf("os=%q want %q", got.OS, tc.os)
			}
		})
	}
}

func TestShouldProbe_OnlyLiveNonDarwin(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_700_000_000_000)
	liveLinux := cluster.NodeSummary{
		State: cluster.StateAlive, OS: "linux", LastUpdatedUnixMs: now.UnixMilli(),
	}
	if !update.ShouldProbe(now, liveLinux) {
		t.Fatal("LIVE linux should probe")
	}
	emptyOS := liveLinux
	emptyOS.OS = ""
	if !update.ShouldProbe(now, emptyOS) {
		t.Fatal("LIVE empty OS should probe (not macos)")
	}
	darwin := liveLinux
	darwin.OS = "darwin"
	if update.ShouldProbe(now, darwin) {
		t.Fatal("LIVE darwin should skip probe")
	}
	stale := liveLinux
	stale.LastUpdatedUnixMs = now.Add(-time.Minute).UnixMilli()
	if update.ShouldProbe(now, stale) {
		t.Fatal("STALE should not probe")
	}
	failed := liveLinux
	failed.State = cluster.StateFailed
	if update.ShouldProbe(now, failed) {
		t.Fatal("FAILED should not probe")
	}
}

func TestLocalInfo_ReadsEnabledAndBusy(t *testing.T) {
	a := &update.Applier{Enabled: true, GOOS: "linux", GOARCH: "amd64", Version: "0.1.0"}
	info := a.LocalInfo()
	if !info.Enabled || info.Busy || info.OS != "linux" || info.Arch != "amd64" || info.Version != "0.1.0" {
		t.Fatalf("%+v", info)
	}
	a.Enabled = false
	info = a.LocalInfo()
	if info.Enabled {
		t.Fatal("enabled should follow Applier.Enabled")
	}
}

func TestLocalInfo_BusyWhenApplyMutexHeld(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	body := linuxTarball(t, "v0.2.0")
	dl := &recordingDownload{body: body, blockStarted: started, blockUntil: unblock}
	rst := &fakeRestarter{}
	a := testApplier(t, dl, rst)
	pin := linuxPin("v0.2.0", "linux/amd64", sha256hex(body))

	done := make(chan error, 1)
	go func() { done <- a.Apply(context.Background(), pin) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("apply finished before download: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for apply to hold mutex")
	}
	info := a.LocalInfo()
	if !info.Busy {
		close(unblock)
		t.Fatal("expected busy while apply holds mutex")
	}
	close(unblock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if a.LocalInfo().Busy {
		t.Fatal("busy should clear after apply")
	}
}

func linuxTarball(t *testing.T, tag string) []byte {
	t.Helper()
	return makeTarGz(t, map[string][]byte{
		"procmesh-agent": []byte("agent-" + tag),
		"procmesh-shim":  []byte("shim-" + tag),
		"procmesh":       []byte("cli-" + tag),
	})
}
