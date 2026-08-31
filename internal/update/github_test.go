package update_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/update"
)

const testChecksumsBody = "" +
	"aaa111  procmesh_0.2.0_linux_amd64.tar.gz\n" +
	"bbb222  procmesh_0.2.0_linux_arm64.tar.gz\n" +
	"ccc333  procmesh_0.2.0_linux_armv7.tar.gz\n"

func errorChainHasURL(err error) bool {
	for err != nil {
		msg := err.Error()
		if strings.Contains(msg, "://") || strings.Contains(msg, "github.com") {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func githubTestServer(t *testing.T, latest http.HandlerFunc, checksums http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	if latest != nil {
		mux.HandleFunc("/repos/owner/procmesh/releases/latest", latest)
	}
	if checksums != nil {
		mux.HandleFunc("/owner/procmesh/releases/download/v0.2.0/checksums.txt", checksums)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubSource_LatestHappyPath(t *testing.T) {
	var checksumHits int
	srv := githubTestServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				t.Errorf("production default is no token; got Authorization=%q", r.Header.Get("Authorization"))
			}
			if err := json.NewEncoder(w).Encode(map[string]any{
				"tag_name":   "v0.2.0",
				"prerelease": false,
				"draft":      false,
			}); err != nil {
				t.Errorf("encode latest: %v", err)
			}
		},
		func(w http.ResponseWriter, r *http.Request) {
			checksumHits++
			if _, err := w.Write([]byte(testChecksumsBody)); err != nil {
				t.Errorf("write checksums: %v", err)
			}
		},
	)

	pin, err := update.GitHubSource{
		Repository:   "owner/procmesh",
		APIBase:      srv.URL,
		DownloadBase: srv.URL,
		HTTPClient:   srv.Client(),
	}.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pin.Repository != "owner/procmesh" || pin.Tag != "v0.2.0" {
		t.Fatalf("pin identity %+v", pin)
	}
	want := map[string]string{
		"linux/amd64": "aaa111",
		"linux/arm64": "bbb222",
		"linux/armv7": "ccc333",
	}
	if len(pin.Checksums) != len(want) {
		t.Fatalf("checksums=%v", pin.Checksums)
	}
	for k, v := range want {
		if pin.Checksums[k] != v {
			t.Fatalf("checksums[%s]=%q want %q full=%v", k, pin.Checksums[k], v, pin.Checksums)
		}
	}
	if checksumHits != 1 {
		t.Fatalf("checksums hits=%d", checksumHits)
	}
}

func TestGitHubSource_RejectsPrereleaseAndDraft(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{"prerelease_flag", map[string]any{"tag_name": "v0.2.0", "prerelease": true, "draft": false}},
		{"prerelease_tag", map[string]any{"tag_name": "v0.2.0-rc.1", "prerelease": false, "draft": false}},
		{"draft", map[string]any{"tag_name": "v0.2.0", "prerelease": false, "draft": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := githubTestServer(t,
				func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewEncoder(w).Encode(tc.body); err != nil {
						t.Errorf("encode: %v", err)
					}
				},
				func(http.ResponseWriter, *http.Request) {
					t.Error("checksums must not be fetched for prerelease/draft")
				},
			)
			_, err := update.GitHubSource{
				Repository:   "owner/procmesh",
				APIBase:      srv.URL,
				DownloadBase: srv.URL,
				HTTPClient:   srv.Client(),
			}.Latest(context.Background())
			if err == nil {
				t.Fatal("expected rejection")
			}
			if !errcode.Is(err, errcode.INVALID) {
				t.Fatalf("err=%v", err)
			}
			if errorChainHasURL(err) {
				t.Fatalf("url leaked: %v", err)
			}
		})
	}
}

func TestGitHubSource_Non200HasNoURL(t *testing.T) {
	srv := githubTestServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "https://api.github.com/repos/owner/procmesh/releases/latest", http.StatusNotFound)
		},
		nil,
	)
	_, err := update.GitHubSource{
		Repository:   "owner/procmesh",
		APIBase:      srv.URL,
		DownloadBase: srv.URL,
		HTTPClient:   srv.Client(),
	}.Latest(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "github http 404") {
		t.Fatalf("want github http 404, got %v", err)
	}
	if errorChainHasURL(err) {
		t.Fatalf("url leaked: %v", err)
	}
	if strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("server url leaked: %v", err)
	}
}

func TestGitHubSource_DownloadAssetPinnedNotLatest(t *testing.T) {
	var latestHits, assetHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/procmesh/releases/latest", func(http.ResponseWriter, *http.Request) {
		latestHits++
	})
	mux.HandleFunc("/owner/procmesh/releases/download/v0.2.0/procmesh_0.2.0_linux_amd64.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		assetHits++
		if _, err := w.Write([]byte("tarball-bytes")); err != nil {
			t.Errorf("write asset: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got, err := update.GitHubSource{
		Repository:   "owner/procmesh",
		APIBase:      srv.URL,
		DownloadBase: srv.URL,
		HTTPClient:   srv.Client(),
	}.DownloadAsset(context.Background(), "v0.2.0", "procmesh_0.2.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "tarball-bytes" {
		t.Fatalf("body=%q", got)
	}
	if latestHits != 0 {
		t.Fatalf("releases/latest hits=%d", latestHits)
	}
	if assetHits != 1 {
		t.Fatalf("asset hits=%d", assetHits)
	}
}

func TestGitHubSource_DownloadAssetDoErrorNoURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	_, err := update.GitHubSource{
		Repository:   "owner/procmesh",
		DownloadBase: base,
	}.DownloadAsset(context.Background(), "v0.2.0", "procmesh_0.2.0_linux_amd64.tar.gz")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("err=%v", err)
	}
	if errorChainHasURL(err) {
		t.Fatalf("url leaked: %v", err)
	}
	if strings.Contains(err.Error(), base) {
		t.Fatalf("url leaked: %v", err)
	}
}

func TestGitHubSource_InvalidRepo(t *testing.T) {
	for _, repo := range []string{"", "noslash", "  "} {
		_, err := update.GitHubSource{Repository: repo}.Latest(context.Background())
		if err == nil {
			t.Fatalf("repo %q: expected error", repo)
		}
		if !errcode.Is(err, errcode.INVALID) {
			t.Fatalf("repo %q: err=%v", repo, err)
		}
	}
}

func TestGitHubSource_DefaultHTTPSNoToken(t *testing.T) {
	var got *http.Request
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Clone(r.Context())
		return nil, errors.New("no network")
	})}
	_, err := update.GitHubSource{
		Repository: "owner/procmesh",
		HTTPClient: client,
	}.Latest(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if got == nil {
		t.Fatal("no request captured")
	}
	if got.URL.Scheme != "https" || got.URL.Host != "api.github.com" {
		t.Fatalf("default API URL=%s", got.URL)
	}
	if got.Header.Get("Authorization") != "" {
		t.Fatalf("token sent: %q", got.Header.Get("Authorization"))
	}
	if errorChainHasURL(err) {
		t.Fatalf("raw Do error must not be wrapped into API-visible message: %v", err)
	}
}

func TestGitHubSource_DoErrorDoesNotWrapURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	_, err := update.GitHubSource{
		Repository:   "owner/procmesh",
		APIBase:      base,
		DownloadBase: base,
	}.Latest(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("err=%v", err)
	}
	if errorChainHasURL(err) {
		t.Fatalf("raw http Do error leaked: %v", err)
	}
	if strings.Contains(err.Error(), base) {
		t.Fatalf("url leaked: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
