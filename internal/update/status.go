package update

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/freshness"
)

// Stable skip_reason codes for ListNodeUpdateStatus.
const (
	SkipSTALE        = "STALE"
	SkipUNKNOWN      = "UNKNOWN"
	SkipFAILED       = "FAILED"
	SkipSUSPECT      = "SUSPECT"
	SkipUNSUPPORTED  = "UNSUPPORTED"
	SkipMACOS        = "MACOS"
	SkipDISABLED     = "DISABLED"
	SkipBUSY         = "BUSY"
	SkipCURRENT      = "CURRENT"
	SkipUNAVAILABLE  = "UNAVAILABLE"
	SkipTIMEOUT      = "TIMEOUT"
	SkipCHECK_FAILED = "CHECK_FAILED"
)

// LocalInfo is the in-app update identity of this Agent.
type LocalInfo struct {
	OS, Arch, Version string
	Enabled, Busy     bool
}

// ProbedInput is gossip identity plus an optional GetLocalUpdateInfo result.
type ProbedInput struct {
	GossipOS, GossipArch, GossipVersion string
	LatestTag                           string
	CheckFailed                         bool
	Info                                *LocalInfo
	ProbeErr                            error
}

// NodeEval is eligibility after a LIVE-node probe (or darwin-from-gossip skip).
type NodeEval struct {
	Eligible          bool
	SkipReason        string
	Busy              bool
	OS, Arch, Version string
}

// LocalInfo reports os/arch/version, whether updates are enabled, and whether
// Apply currently holds the package mutex. Nil receiver uses runtime defaults
// with Enabled=false.
func (a *Applier) LocalInfo() LocalInfo {
	return LocalInfo{
		OS:      a.goos(),
		Arch:    a.goarch(),
		Version: a.version(),
		Enabled: a != nil && a.Enabled,
		Busy:    ApplyBusy(),
	}
}

// ApplyBusy reports whether Apply currently holds the package mutex.
func ApplyBusy() bool {
	if !applyMu.TryLock() {
		return true
	}
	applyMu.Unlock()
	return false
}

// SkipNotLive maps a non-LIVE member to a skip_reason. FAILED/SUSPECT win over
// freshness STALE/UNKNOWN so those states keep stable codes.
func SkipNotLive(state, classified string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "FAILED":
		return SkipFAILED
	case "SUSPECT":
		return SkipSUSPECT
	}
	if classified == freshness.UNKNOWN {
		return SkipUNKNOWN
	}
	return SkipSTALE
}

// Evaluate classifies a member for cluster rolling update eligibility.
// Non-LIVE members are skipped without probing; LIVE darwin is MACOS.
func Evaluate(now time.Time, member cluster.NodeSummary, latestTag string, checkFailed bool, info *LocalInfo, probeErr error) NodeEval {
	classified := freshness.Classify(now, member.LastUpdatedUnixMs, string(member.State))
	if classified != freshness.LIVE {
		return NodeEval{
			OS: member.OS, Arch: member.Arch, Version: member.AgentVersion,
			SkipReason: SkipNotLive(string(member.State), classified),
		}
	}
	if !ShouldProbe(now, member) {
		return NodeEval{
			OS: member.OS, Arch: member.Arch, Version: member.AgentVersion,
			SkipReason: SkipMACOS,
		}
	}
	return EvaluateProbed(ProbedInput{
		GossipOS: member.OS, GossipArch: member.Arch, GossipVersion: member.AgentVersion,
		LatestTag: latestTag, CheckFailed: checkFailed, Info: info, ProbeErr: probeErr,
	})
}

// ShouldProbe reports whether a LIVE non-darwin member should be probed.
// Empty OS is not treated as macOS.
func ShouldProbe(now time.Time, member cluster.NodeSummary) bool {
	classified := freshness.Classify(now, member.LastUpdatedUnixMs, string(member.State))
	if classified != freshness.LIVE {
		return false
	}
	return !isDarwin(member.OS)
}

// EvaluateProbed classifies a LIVE member after an optional GetLocalUpdateInfo
// probe. UNSUPPORTED is only for unimplemented/unknown-method RPCs; LIVE peer
// timeouts and unavailability keep distinct skip codes. Empty latest tag or a
// failed CheckLatest is CHECK_FAILED and never eligible. Empty os/arch is never MACOS.
func EvaluateProbed(in ProbedInput) NodeEval {
	os := coalesce(field(in.Info, func(i *LocalInfo) string { return i.OS }), in.GossipOS)
	arch := coalesce(field(in.Info, func(i *LocalInfo) string { return i.Arch }), in.GossipArch)
	ver := coalesce(field(in.Info, func(i *LocalInfo) string { return i.Version }), in.GossipVersion)
	out := NodeEval{OS: os, Arch: arch, Version: ver}
	if in.Info != nil {
		out.Busy = in.Info.Busy
	}
	if in.ProbeErr != nil {
		out.SkipReason = ClassifyProbeError(in.ProbeErr)
		return out
	}
	if isDarwin(os) {
		out.SkipReason = SkipMACOS
		return out
	}
	if in.Info != nil && !in.Info.Enabled {
		out.SkipReason = SkipDISABLED
		return out
	}
	if in.Info != nil && in.Info.Busy {
		out.SkipReason = SkipBUSY
		return out
	}
	if in.CheckFailed || strings.TrimSpace(in.LatestTag) == "" {
		out.SkipReason = SkipCHECK_FAILED
		return out
	}
	if !IsBehind(ver, in.LatestTag) {
		out.SkipReason = SkipCURRENT
		return out
	}
	out.Eligible = true
	return out
}

// ClassifyProbeError maps a LIVE-peer GetLocalUpdateInfo failure to a skip_reason.
// Unimplemented / unknown-method RPCs are UNSUPPORTED; timeouts are TIMEOUT;
// every other probe failure is UNAVAILABLE.
func ClassifyProbeError(err error) string {
	if err == nil {
		return ""
	}
	if isUnimplementedRPC(err) {
		return SkipUNSUPPORTED
	}
	if isProbeTimeout(err) {
		return SkipTIMEOUT
	}
	return SkipUNAVAILABLE
}

func isUnimplementedRPC(err error) bool {
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown method") || strings.Contains(msg, "unimplemented")
}

func isProbeTimeout(err error) bool {
	if errcode.Is(err, errcode.TIMEOUT) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return connect.CodeOf(err) == connect.CodeDeadlineExceeded
}

func isDarwin(os string) bool {
	return strings.EqualFold(strings.TrimSpace(os), "darwin")
}

func coalesce(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func field(info *LocalInfo, get func(*LocalInfo) string) string {
	if info == nil {
		return ""
	}
	return get(info)
}
