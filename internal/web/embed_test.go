package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
