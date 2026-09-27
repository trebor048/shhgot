package main

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
)

// The React dashboard is built by `npm run build` in ./web and the compiled
// bundle is committed under ./web/dist. Embedding the built assets keeps the
// project's "one static binary, nothing to deploy" property: `go build` alone
// produces a binary that serves the full UI with no Node toolchain present.
//
//go:embed all:web/dist
var webDistFS embed.FS

var (
	webUIIndex    []byte
	webUIAssets   http.Handler
	webUILegacyOn = envTruthy(os.Getenv("SHHGIT_LEGACY_UI"))
)

func init() {
	sub, err := fs.Sub(webDistFS, "web/dist")
	if err != nil {
		// Leave the SPA handlers nil; serveWebUI falls back to the legacy
		// dashboard so a broken build cannot leave the operator with no UI.
		return
	}
	if b, err := fs.ReadFile(sub, "index.html"); err == nil {
		webUIIndex = b
	}
	webUIAssets = http.FileServer(http.FS(sub))
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// uiLegacyRequested reports whether the caller asked for the original
// single-file dashboard, either for the whole process (SHHGIT_LEGACY_UI) or for
// this one request (?legacy=1).
func uiLegacyRequested(r *http.Request) bool {
	return webUILegacyOn || envTruthy(r.URL.Query().Get("legacy"))
}

// serveWebUI serves the embedded React bundle.
//
// Only "/" and "/assets/..." are served. API typos still answer with a JSON 404
// (matching every other /api/... route) and anything else is a plain 404, so a
// missing route cannot masquerade as a successful HTML page - the same contract
// the previous dashboard held.
func serveWebUI(w http.ResponseWriter, r *http.Request) {
	if webUIIndex == nil || webUIAssets == nil {
		serveLegacyDashboard(w, r)
		return
	}

	switch {
	case r.URL.Path == "/" || r.URL.Path == "/index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Never cache the shell: a stale index.html references asset hashes that
		// no longer exist after an upgrade.
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(webUIIndex)

	case strings.HasPrefix(r.URL.Path, "/assets/"):
		// Vite emits content-hashed filenames, so a long immutable cache is safe.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		webUIAssets.ServeHTTP(w, r)

	case r.URL.Path == "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)

	case strings.HasPrefix(r.URL.Path, "/api/"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"no such endpoint","path":%q}`, r.URL.Path)

	default:
		http.NotFound(w, r)
	}
}

// serveLegacyDashboard serves the original self-contained dashboard embedded
// from dashboard/index.html. It remains the fallback if the React bundle is
// missing from the build and is reachable on request with ?legacy=1.
func serveLegacyDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		switch {
		case r.URL.Path == "/favicon.ico":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"no such endpoint","path":%q}`, r.URL.Path)
		default:
			http.NotFound(w, r)
		}
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, dashboardHTML)
}
