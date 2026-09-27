package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests pin the contract of the embedded React dashboard: the shell is
// served from "/", hashed assets load from "/assets/...", the ?legacy=1 escape
// hatch still serves the original single-file dashboard, and unknown API paths
// answer with a JSON 404 instead of HTML.

func testWebMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	registerRoutes(mux)
	return mux
}

func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestEmbeddedSPAIsServed(t *testing.T) {
	if webUIIndex == nil {
		t.Fatal("web/dist/index.html was not embedded; run `npm run build` in cmd/shhgit/web and rebuild")
	}

	rec := get(t, testWebMux(t), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET / Content-Type = %q, want text/html", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("GET / Cache-Control = %q, want no-store", cc)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="root"`) {
		t.Fatalf("dashboard shell does not contain the React root element:\n%s", body)
	}

	// Pull an asset URL out of the shell and confirm it resolves from the embed.
	m := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("dashboard shell references no /assets/ bundle:\n%s", body)
	}
	asset := get(t, testWebMux(t), m[1])
	if asset.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", m[1], asset.Code)
	}
	if asset.Body.Len() == 0 {
		t.Fatalf("GET %s returned an empty body", m[1])
	}
	if cc := asset.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("GET %s Cache-Control = %q, want immutable", m[1], cc)
	}
}

func TestLegacyDashboardEscapeHatch(t *testing.T) {
	rec := get(t, testWebMux(t), "/?legacy=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /?legacy=1 = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `id="root"`) {
		t.Fatal("?legacy=1 served the React shell instead of the legacy dashboard")
	}
	if !strings.Contains(body, `id="panel-matches"`) {
		t.Fatal("?legacy=1 did not serve the legacy dashboard")
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	rec := get(t, testWebMux(t), "/api/definitely-not-a-route")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown API path = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("unknown API path Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("unknown API path body = %q, want a JSON error", rec.Body.String())
	}
}

func TestUnknownNonAPIPathIsNotFound(t *testing.T) {
	rec := get(t, testWebMux(t), "/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", rec.Code)
	}
}

func TestFaviconIsNoContent(t *testing.T) {
	rec := get(t, testWebMux(t), "/favicon.ico")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("GET /favicon.ico = %d, want 204", rec.Code)
	}
}

// TestLiveServerServesSPA boots the real server (no scanner) on a loopback port
// and fetches the dashboard, an asset and an API route over a socket. This is
// the only test that exercises ListenAndServe, the embed and the route table
// together exactly as a browser would hit them.
func TestLiveServerServesSPA(t *testing.T) {
	// Reserve a free port, then hand it to StartWebServer.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not reserve a port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	go func() { _ = StartWebServer("127.0.0.1", strconv.Itoa(port)) }()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: 2 * time.Second}

	// Wait for the listener to accept connections.
	var body string
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Get(base + "/")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				body = string(b)
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become ready: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !strings.Contains(body, `id="root"`) {
		t.Fatalf("live server did not serve the React shell:\n%s", body)
	}

	if m := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindStringSubmatch(body); m != nil {
		resp, err := client.Get(base + m[1])
		if err != nil {
			t.Fatalf("GET %s: %v", m[1], err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s over the wire = %d, want 200", m[1], resp.StatusCode)
		}
	} else {
		t.Fatal("live shell references no asset bundle")
	}

	// API routes must still answer over the wire on the same listener.
	resp, err := client.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", resp.StatusCode)
	}

	resp, err = client.Get(base + "/api/matches")
	if err != nil {
		t.Fatalf("GET /api/matches: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/matches = %d, want 200 (localGuard should allow a loopback request with no Origin)", resp.StatusCode)
	}
}
