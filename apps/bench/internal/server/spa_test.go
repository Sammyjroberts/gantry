package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestSPAHandler covers the three routing outcomes: a real asset is served as a
// file, a client-route deep link falls back to index.html with 200, and a
// missing asset (a path with an extension) 404s.
//
// Hermetic on purpose: it runs against an in-memory FS, not the embedded
// ui.FS() — the committed embed dir holds only the placeholder index.html
// (real build assets are gitignored and appear locally after `just bench-release`),
// so asserting against a real bundle filename passes locally and 404s in CI.
func TestSPAHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte(`<!doctype html><html><body><div id="root"></div></body></html>`)},
		"assets/app.js": {Data: []byte(`console.log("app")`)},
	}
	h, err := newSPAHandler(fsys)
	if err != nil {
		t.Fatalf("newSPAHandler: %v", err)
	}

	indexBody := getBody(t, h, "/")
	if !strings.Contains(indexBody, `id="root"`) {
		t.Fatalf("root did not serve index.html; body=%q", indexBody)
	}

	// 1. A real JS asset is served as a file with a JS content-type (not the
	//    HTML fallback).
	rec := doGet(h, "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("asset content-type = %q, want javascript", ct)
	}
	if body := rec.Body.String(); strings.Contains(body, `id="root"`) {
		t.Fatalf("asset request returned the index.html fallback, want the JS file")
	}

	// 2. A client-route deep link (no extension, no real file) → index.html, 200.
	rec = doGet(h, "/hardware/foo")
	if rec.Code != http.StatusOK {
		t.Fatalf("deep-link status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("deep-link content-type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), `id="root"`) {
		t.Fatalf("deep link did not serve index.html; body=%q", rec.Body.String())
	}

	// 3. A missing asset (has an extension) → 404, not the SPA fallback.
	rec = doGet(h, "/nonexistent.png")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing-asset status = %d, want 404", rec.Code)
	}
}

func doGet(h http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getBody(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := doGet(h, target)
	b, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
