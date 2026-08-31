package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/version"
	"golang.org/x/mod/semver"
)

// DownloadTimeout is the maximum time allowed for download and checksum verify.
const DownloadTimeout = 5 * time.Minute

var applyMu sync.Mutex

// DownloadFunc fetches a pinned GitHub release asset. Tests inject fakes.
// repository/tag/asset are passed separately so implementations can avoid
// putting URLs in returned errors.
type DownloadFunc func(ctx context.Context, repository, tag, asset string) ([]byte, error)

// Installer extracts the three binaries, keeps one previous bundle, and
// replaces files in destDir via temp+rename.
type Installer interface {
	Install(ctx context.Context, tarball []byte, destDir, previousDir string) error
}

// Restarter restarts the agent process after binaries are replaced.
type Restarter interface {
	Restart(ctx context.Context) error
}

// Applier downloads, verifies, installs, and restarts a pinned agent update.
type Applier struct {
	Enabled      bool
	DataDir      string
	Version      string // current version.Agent
	GOOS, GOARCH string
	// BinDir overrides the running executable directory (tests). Empty uses os.Executable.
	BinDir       string
	Download     DownloadFunc
	HTTPClient   *http.Client
	DownloadBase string
	Installer    Installer
	Restarter    Restarter
}

func (a *Applier) goos() string {
	if a != nil && a.GOOS != "" {
		return a.GOOS
	}
	return runtime.GOOS
}

func (a *Applier) goarch() string {
	if a != nil && a.GOARCH != "" {
		return a.GOARCH
	}
	return runtime.GOARCH
}

func (a *Applier) version() string {
	if a != nil && a.Version != "" {
		return a.Version
	}
	return version.Agent
}

func (a *Applier) binDir() (string, error) {
	if a != nil && a.BinDir != "" {
		return a.BinDir, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", errcode.E(errcode.UNAVAILABLE, "resolve agent executable")
	}
	return filepath.Dir(exe), nil
}

// PlatformKey is the checksums map key for goos/goarch.
// runtime.GOARCH "arm" maps to "armv7".
func PlatformKey(goos, goarch string) string {
	return goos + "/" + linuxArch(goarch)
}

func linuxArch(goarch string) string {
	if goarch == "arm" {
		return "armv7"
	}
	return goarch
}

func linuxAssetName(tag, goarch string) (string, error) {
	ver := StripV(strings.TrimSpace(tag))
	if ver == "" {
		return "", errcode.E(errcode.INVALID, "release tag required")
	}
	arch := linuxArch(goarch)
	switch arch {
	case "amd64", "arm64", "armv7":
		return "procmesh_" + ver + "_linux_" + arch + ".tar.gz", nil
	default:
		return "", errcode.E(errcode.INVALID, "unsupported architecture")
	}
}

// VersionsEqual reports whether current and pinTag name the same release.
func VersionsEqual(current, pinTag string) bool {
	cur := strings.TrimSpace(current)
	lat := strings.TrimSpace(pinTag)
	if cur == "" && lat == "" {
		return true
	}
	cv := canonicalSemver(cur)
	lv := canonicalSemver(lat)
	if semver.IsValid(cv) && semver.IsValid(lv) {
		return semver.Compare(cv, lv) == 0
	}
	return StripV(cur) == StripV(lat)
}

// Apply downloads the pinned tarball for this node, verifies it, replaces the
// three binaries, keeps one previous bundle, and restarts the agent.
func (a *Applier) Apply(ctx context.Context, pin Pin) error {
	if !applyMu.TryLock() {
		return errcode.E(errcode.CONFLICT, "update already in progress")
	}
	defer applyMu.Unlock()
	if a == nil {
		return errcode.E(errcode.UNAVAILABLE, "update applier unavailable")
	}
	if !a.Enabled {
		return errcode.E(errcode.DENIED, "updates are disabled")
	}
	if a.goos() != "linux" {
		return errcode.E(errcode.INVALID, "update is only supported on linux")
	}
	if strings.TrimSpace(pin.Tag) == "" {
		return errcode.E(errcode.INVALID, "release tag required")
	}
	if VersionsEqual(a.version(), pin.Tag) {
		return nil
	}

	asset, err := linuxAssetName(pin.Tag, a.goarch())
	if err != nil {
		return err
	}
	key := PlatformKey("linux", a.goarch())
	sum := ""
	if pin.Checksums != nil {
		sum = pin.Checksums[key]
	}
	if strings.TrimSpace(sum) == "" {
		return errcode.E(errcode.INVALID, "missing checksum for platform")
	}
	if strings.TrimSpace(pin.Repository) == "" {
		return errcode.E(errcode.INVALID, "update repository required")
	}

	body, err := a.downloadAndVerify(ctx, pin.Repository, pin.Tag, asset, sum)
	if err != nil {
		return err
	}

	dest, err := a.binDir()
	if err != nil {
		return err
	}
	if strings.TrimSpace(a.DataDir) == "" {
		return errcode.E(errcode.INVALID, "data dir required")
	}
	prev := filepath.Join(a.DataDir, "update", "previous")
	inst := a.Installer
	if inst == nil {
		inst = TarInstaller{}
	}
	if err := inst.Install(ctx, body, dest, prev); err != nil {
		return mapApplyErr(err)
	}

	rst := a.Restarter
	if rst == nil {
		rst = AgentRestarter{AgentPath: filepath.Join(dest, "procmesh-agent")}
	}
	if err := rst.Restart(ctx); err != nil {
		return mapApplyErr(err)
	}
	return nil
}

// downloadAndVerify applies DownloadTimeout to fetch + checksum only.
func (a *Applier) downloadAndVerify(ctx context.Context, repo, tag, asset, sum string) ([]byte, error) {
	dlCtx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()

	body, err := a.download(dlCtx, repo, tag, asset)
	if err != nil {
		return nil, mapApplyErr(err)
	}
	if err := verifySHA256(body, sum); err != nil {
		return nil, err
	}
	if err := dlCtx.Err(); err != nil {
		return nil, mapApplyErr(err)
	}
	return body, nil
}

func (a *Applier) download(ctx context.Context, repo, tag, asset string) ([]byte, error) {
	if a.Download != nil {
		return a.Download(ctx, repo, tag, asset)
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DownloadTimeout}
	}
	return GitHubSource{
		Repository:   repo,
		HTTPClient:   client,
		DownloadBase: a.DownloadBase,
	}.DownloadAsset(ctx, tag, asset)
}

func verifySHA256(data []byte, want string) error {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return errcode.E(errcode.INVALID, "checksum required")
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return errcode.E(errcode.INVALID, "checksum mismatch")
	}
	return nil
}

func mapApplyErr(err error) error {
	if err == nil {
		return nil
	}
	if errcode.Is(err, errcode.CONFLICT) || errcode.Is(err, errcode.DENIED) ||
		errcode.Is(err, errcode.INVALID) || errcode.Is(err, errcode.TIMEOUT) ||
		errcode.Is(err, errcode.UNAVAILABLE) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errcode.E(errcode.TIMEOUT, "update timed out")
	}
	if errors.Is(err, context.Canceled) {
		return errcode.E(errcode.UNAVAILABLE, "update canceled")
	}
	return errcode.E(errcode.UNAVAILABLE, "update failed")
}
