package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const maxReleaseAssetBytes = 256 << 20

const defaultHTTPTimeout = 15 * time.Second

// GitHubSource loads the latest non-prerelease release and checksums.txt over HTTPS.
type GitHubSource struct {
	Repository string
	HTTPClient *http.Client
	// APIBase and DownloadBase override GitHub URLs (tests only).
	APIBase      string
	DownloadBase string
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

func (s GitHubSource) client() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}

func (s GitHubSource) apiBase() string {
	if s.APIBase != "" {
		return strings.TrimRight(s.APIBase, "/")
	}
	return "https://api.github.com"
}

func (s GitHubSource) downloadBase() string {
	if s.DownloadBase != "" {
		return strings.TrimRight(s.DownloadBase, "/")
	}
	return "https://github.com"
}

// Latest fetches /releases/latest and checksums.txt for the repository.
func (s GitHubSource) Latest(ctx context.Context) (Pin, error) {
	repo := strings.TrimSpace(s.Repository)
	if repo == "" || !strings.Contains(repo, "/") {
		return Pin{}, errcode.E(errcode.INVALID, "update repository must be owner/repo")
	}

	relURL := fmt.Sprintf("%s/repos/%s/releases/latest", s.apiBase(), repo)
	var rel githubRelease
	if err := s.getJSON(ctx, relURL, &rel); err != nil {
		return Pin{}, err
	}
	if rel.Draft || rel.Prerelease || IsPrereleaseTag(rel.TagName) {
		return Pin{}, errcode.E(errcode.INVALID, "prerelease tag ignored")
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return Pin{}, errcode.E(errcode.INVALID, "release tag required")
	}

	sumURL := fmt.Sprintf("%s/%s/releases/download/%s/checksums.txt", s.downloadBase(), repo, rel.TagName)
	body, err := s.getBytes(ctx, sumURL)
	if err != nil {
		return Pin{}, err
	}
	sums, err := ParseChecksums(string(body), StripV(rel.TagName))
	if err != nil {
		return Pin{}, err
	}
	return Pin{Repository: repo, Tag: rel.TagName, Checksums: sums}, nil
}

// DownloadAsset fetches a pinned release tarball. It never calls /releases/latest.
func (s GitHubSource) DownloadAsset(ctx context.Context, tag, asset string) ([]byte, error) {
	repo := strings.TrimSpace(s.Repository)
	tag = strings.TrimSpace(tag)
	asset = strings.TrimSpace(asset)
	if repo == "" || !strings.Contains(repo, "/") {
		return nil, errcode.E(errcode.INVALID, "update repository must be owner/repo")
	}
	if tag == "" {
		return nil, errcode.E(errcode.INVALID, "release tag required")
	}
	if asset == "" || strings.Contains(asset, "/") || strings.Contains(asset, "..") {
		return nil, errcode.E(errcode.INVALID, "invalid release asset")
	}
	url := fmt.Sprintf("%s/%s/releases/download/%s/%s", s.downloadBase(), repo, tag, asset)
	return s.getAssetBytes(ctx, url)
}

func (s GitHubSource) assetClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: DownloadTimeout}
}

func (s GitHubSource) getAssetBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errcode.E(errcode.UNAVAILABLE, "github request failed")
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "procmesh-agent")

	res, err := s.assetClient().Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		return nil, errcode.E(errcode.UNAVAILABLE, "github request failed")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxReleaseAssetBytes+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		return nil, errcode.E(errcode.UNAVAILABLE, "github response read failed")
	}
	if len(body) > maxReleaseAssetBytes {
		return nil, errcode.E(errcode.INVALID, "release asset too large")
	}
	if res.StatusCode != http.StatusOK {
		return nil, errcode.E(errcode.UNAVAILABLE, fmt.Sprintf("github http %d", res.StatusCode))
	}
	return body, nil
}

func (s GitHubSource) getJSON(ctx context.Context, url string, dst any) error {
	body, err := s.getBytes(ctx, url)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return errcode.E(errcode.UNAVAILABLE, "github release decode failed")
	}
	return nil
}

func (s GitHubSource) getBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errcode.E(errcode.UNAVAILABLE, "github request failed")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "procmesh-agent")

	res, err := s.client().Do(req)
	if err != nil {
		// Do not wrap the net/http error: it includes the request URL.
		return nil, errcode.E(errcode.UNAVAILABLE, "github request failed")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, errcode.E(errcode.UNAVAILABLE, "github response read failed")
	}
	if res.StatusCode != http.StatusOK {
		return nil, errcode.E(errcode.UNAVAILABLE, fmt.Sprintf("github http %d", res.StatusCode))
	}
	return body, nil
}
