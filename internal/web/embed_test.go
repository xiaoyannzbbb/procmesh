package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
