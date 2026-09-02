package web

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler_Root(t *testing.T) {
	handler := Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "ProcMesh")
	assert.Contains(t, body, "<!doctype html>")
}

func TestHandler_SPAFallback(t *testing.T) {
	handler := Handler()

	tests := []string{
		"/nodes",
		"/nodes/abc",
		"/processes",
		"/users",
		"/audit",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			body := w.Body.String()
			assert.Contains(t, body, "ProcMesh")
			assert.Contains(t, body, `<div id="app">`)
		})
	}
}

func TestHandler_MissingAsset(t *testing.T) {
	handler := Handler()

	req := httptest.NewRequest("GET", "/assets/missing.js", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// 资源不存在，回退到 index.html
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "ProcMesh")
	assert.Contains(t, body, `<div id="app">`)
}

func TestHasIndex(t *testing.T) {
	assert.True(t, HasIndex())
}

func TestHandler_ServesPrecompressedAssets(t *testing.T) {
	assets, err := fs.Glob(dist, "dist/assets/*.js")
	require.NoError(t, err)
	require.NotEmpty(t, assets)

	asset := strings.TrimPrefix(assets[0], "dist")
	req := httptest.NewRequest(http.MethodGet, asset, nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.Contains(t, rec.Header().Values("Vary"), "Accept-Encoding")
	assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))

	zr, err := gzip.NewReader(rec.Body)
	require.NoError(t, err)
	uncompressed, err := io.ReadAll(zr)
	require.NoError(t, err)
	require.NoError(t, zr.Close())
	original, err := dist.ReadFile("dist" + asset)
	require.NoError(t, err)
	assert.Equal(t, original, uncompressed)
}

func TestHandler_RespectsDisabledGzip(t *testing.T) {
	assets, err := fs.Glob(dist, "dist/assets/*.js")
	require.NoError(t, err)
	require.NotEmpty(t, assets)

	asset := strings.TrimPrefix(assets[0], "dist")
	req := httptest.NewRequest(http.MethodGet, asset, nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0, identity")
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	original, err := dist.ReadFile("dist" + asset)
	require.NoError(t, err)
	assert.Equal(t, original, rec.Body.Bytes())
}

func TestHandler_GzipHeadAndRange(t *testing.T) {
	assets, err := fs.Glob(dist, "dist/assets/*.js")
	require.NoError(t, err)
	require.NotEmpty(t, assets)
	asset := strings.TrimPrefix(assets[0], "dist")

	headReq := httptest.NewRequest(http.MethodHead, asset, nil)
	headReq.Header.Set("Accept-Encoding", "gzip")
	headRec := httptest.NewRecorder()
	Handler().ServeHTTP(headRec, headReq)
	require.Equal(t, http.StatusOK, headRec.Code)
	assert.Equal(t, "gzip", headRec.Header().Get("Content-Encoding"))
	assert.Empty(t, headRec.Body.Bytes())

	rangeReq := httptest.NewRequest(http.MethodGet, asset, nil)
	rangeReq.Header.Set("Accept-Encoding", "gzip")
	rangeReq.Header.Set("Range", "bytes=0-3")
	rangeRec := httptest.NewRecorder()
	Handler().ServeHTTP(rangeRec, rangeReq)
	require.Equal(t, http.StatusPartialContent, rangeRec.Code)
	assert.Empty(t, rangeRec.Header().Get("Content-Encoding"))
	original, err := dist.ReadFile("dist" + asset)
	require.NoError(t, err)
	assert.Equal(t, original[:4], rangeRec.Body.Bytes())
}

func TestHandler_CachePolicy(t *testing.T) {
	for _, path := range []string{"/", "/nodes", "/locales/en/common.json", "/favicon.svg"} {
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"), path)
	}
}

func TestEmbed_LazyBackupAssets(t *testing.T) {
	for locale, want := range map[string]string{
		"en": "Backup time",
		"zh": "备份时间",
	} {
		t.Run(locale, func(t *testing.T) {
			body, err := dist.ReadFile("dist/locales/" + locale + "/features.json")
			require.NoError(t, err)

			var messages map[string]any
			require.NoError(t, json.Unmarshal(body, &messages))
			backup, ok := messages["backup"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, want, backup["backupTime"])
			assert.NotContains(t, backup, "lastUpdated")
		})
	}

	assets, err := fs.Glob(dist, "dist/assets/index-*.js")
	require.NoError(t, err)
	require.Len(t, assets, 1)
	bundle, err := dist.ReadFile(assets[0])
	require.NoError(t, err)
	assert.NotContains(t, string(bundle), "backup.backupTime")
	assert.NotContains(t, string(bundle), "Backup time")
	assert.NotContains(t, string(bundle), "备份时间")
	assert.NotContains(t, string(bundle), "backup.lastUpdated")
}

func TestEmbed_HandlerSPAFallback(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/nodes/abc", "/assets/missing.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s %d", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `<div id="app">`) {
			t.Fatalf("GET %s body %q", path, body)
		}
		if path != "/" && body != indexBody(t) {
			t.Fatalf("GET %s want same index as /", path)
		}
	}
}

func indexBody(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / %d", rec.Code)
	}
	return rec.Body.String()
}
