package update

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/freshness"
	"golang.org/x/mod/semver"
)

const DefaultCacheTTL = 15 * time.Minute

// Pin identifies a concrete release artifact set.
type Pin struct {
	Repository string
	Tag        string
	Checksums  map[string]string // "linux/amd64" -> sha256
}

// Result is the outcome of CheckLatest, including cache and error metadata.
type Result struct {
	Pin           Pin
	CheckedUnixMs int64
	FromCache     bool
	CheckError    bool
	ErrorMessage  string
}

// ReleaseSource fetches the latest stable release pin.
type ReleaseSource interface {
	Latest(ctx context.Context) (Pin, error)
}

// Checker caches latest-release lookups for DefaultCacheTTL.
type Checker struct {
	Source ReleaseSource
	Now    func() time.Time
	TTL    time.Duration

	mu     sync.Mutex
	cached *cachedPin
}

type cachedPin struct {
	pin       Pin
	checkedAt time.Time
}

func NewChecker(src ReleaseSource, now func() time.Time) *Checker {
	if now == nil {
		now = time.Now
	}
	return &Checker{Source: src, Now: now, TTL: DefaultCacheTTL}
}

func (c *Checker) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultCacheTTL
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// CheckLatest returns the latest stable pin. refresh bypasses a warm cache.
// Source failures are never treated as "up to date": with no usable cache they
// return an error; with an unexpired cache they return the cache and CheckError.
func (c *Checker) CheckLatest(ctx context.Context, refresh bool) (Result, error) {
	if c == nil || c.Source == nil {
		return Result{CheckError: true, ErrorMessage: "update source unavailable"},
			errcode.E(errcode.UNAVAILABLE, "update source unavailable")
	}

	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if !refresh {
		if res, ok := c.cacheHit(now); ok {
			return res, nil
		}
	}

	pin, err := c.Source.Latest(ctx)
	if err == nil {
		if err = ValidatePin(pin); err == nil {
			checkedAt := now
			c.cached = &cachedPin{pin: clonePin(pin), checkedAt: checkedAt}
			return Result{
				Pin:           clonePin(pin),
				CheckedUnixMs: checkedAt.UnixMilli(),
			}, nil
		}
	}

	msg := publicSourceErrorMessage(err)
	if res, ok := c.cacheHit(now); ok {
		res.CheckError = true
		res.ErrorMessage = msg
		return res, nil
	}
	return Result{CheckError: true, ErrorMessage: msg}, wrapSourceErr(err)
}

func publicSourceErrorMessage(_ error) string {
	return "update source failed"
}

func (c *Checker) cacheHit(now time.Time) (Result, bool) {
	if c.cached == nil {
		return Result{}, false
	}
	if now.Sub(c.cached.checkedAt) >= c.ttl() {
		return Result{}, false
	}
	return Result{
		Pin:           clonePin(c.cached.pin),
		CheckedUnixMs: c.cached.checkedAt.UnixMilli(),
		FromCache:     true,
	}, true
}

func wrapSourceErr(err error) error {
	if err == nil {
		return errcode.E(errcode.UNAVAILABLE, "update source failed")
	}
	if errcode.Is(err, errcode.UNAVAILABLE) || errcode.Is(err, errcode.INVALID) {
		return err
	}
	return errcode.E(errcode.UNAVAILABLE, "update source failed")
}

// ValidatePin checks repository, stable tag, and required platform checksums.
func ValidatePin(pin Pin) error {
	if pin.Repository == "" {
		return errcode.E(errcode.INVALID, "update repository required")
	}
	if pin.Tag == "" {
		return errcode.E(errcode.INVALID, "release tag required")
	}
	if IsPrereleaseTag(pin.Tag) {
		return errcode.E(errcode.INVALID, "prerelease tag ignored")
	}
	for _, arch := range requiredArches {
		if pin.Checksums[arch] == "" {
			return errcode.E(errcode.INVALID, "missing checksum for "+arch)
		}
	}
	return nil
}

func clonePin(p Pin) Pin {
	out := Pin{Repository: p.Repository, Tag: p.Tag}
	if p.Checksums != nil {
		out.Checksums = make(map[string]string, len(p.Checksums))
		for k, v := range p.Checksums {
			out.Checksums[k] = v
		}
	}
	return out
}

// StripV removes a single leading v/V from a version tag.
func StripV(s string) string {
	if len(s) > 0 && (s[0] == 'v' || s[0] == 'V') {
		return s[1:]
	}
	return s
}

func canonicalSemver(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if s[0] == 'v' || s[0] == 'V' {
		return "v" + s[1:]
	}
	return "v" + s
}

// IsPrereleaseTag reports whether tag is a semver prerelease (e.g. v1.2.3-rc.1).
func IsPrereleaseTag(tag string) bool {
	v := canonicalSemver(tag)
	if !semver.IsValid(v) {
		return false
	}
	return semver.Prerelease(v) != ""
}

// IsBehind reports whether current should be treated as behind latest.
// Semver tags (after stripping leading v) use semver order; otherwise inequality
// of the stripped strings means "different"/behind. current >= latest → false.
func IsBehind(current, latest string) bool {
	cur := strings.TrimSpace(current)
	lat := strings.TrimSpace(latest)
	if cur == "" && lat == "" {
		return false
	}
	cv := canonicalSemver(cur)
	lv := canonicalSemver(lat)
	if semver.IsValid(cv) && semver.IsValid(lv) {
		return semver.Compare(cv, lv) < 0
	}
	return StripV(cur) != StripV(lat)
}

// AnyLiveLinuxBehind reports whether any freshness-LIVE linux member is behind latestTag.
// LIVE is ALIVE and last_updated within freshness.MaxAge. Empty OS is not linux.
func AnyLiveLinuxBehind(members []cluster.NodeSummary, latestTag string, now time.Time) bool {
	for _, m := range members {
		if freshness.Classify(now, m.LastUpdatedUnixMs, string(m.State)) != freshness.LIVE {
			continue
		}
		if m.OS != "linux" {
			continue
		}
		if IsBehind(m.AgentVersion, latestTag) {
			return true
		}
	}
	return false
}
