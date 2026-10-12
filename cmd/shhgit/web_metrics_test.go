package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core"
)

// /api/metrics (no format) returns the collector's snapshot as JSON.
func TestMetricsEndpointJSON(t *testing.T) {
	core.DefaultMetrics.Reset()
	core.DefaultMetrics.ObserveLatency(core.SubsystemScan, 5*time.Millisecond)
	core.DefaultMetrics.IncOperations(core.SubsystemScan)
	core.DefaultMetrics.IncError(core.SubsystemScan)

	rec := httptest.NewRecorder()
	getMetrics(rec, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want JSON", ct)
	}

	var body map[string]core.LatencyStats
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/metrics: %v", err)
	}
	scan, ok := body[core.SubsystemScan]
	if !ok {
		t.Fatalf("snapshot missing %q subsystem: %v", core.SubsystemScan, body)
	}
	if scan.Ops != 1 || scan.Errors != 1 || scan.Count != 1 {
		t.Fatalf("scan stats = %+v, want 1 op / 1 error / 1 sample", scan)
	}
	if scan.P50 <= 0 || scan.P95 <= 0 {
		t.Fatalf("scan percentiles = %+v, want positive values", scan)
	}
}

// ?subsystem=<name> narrows the JSON snapshot to a single subsystem.
func TestMetricsEndpointSubsystemFilter(t *testing.T) {
	core.DefaultMetrics.Reset()
	core.DefaultMetrics.IncOperations(core.SubsystemValidation)

	rec := httptest.NewRecorder()
	getMetrics(rec, httptest.NewRequest(http.MethodGet, "/api/metrics?subsystem=validation", nil))

	var body map[string]core.LatencyStats
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/metrics: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("subsystem filter returned %d subsystems, want 1: %v", len(body), body)
	}
	if body[core.SubsystemValidation].Ops != 1 {
		t.Fatalf("validation ops = %d, want 1", body[core.SubsystemValidation].Ops)
	}
}

// ?format=prometheus returns the text exposition format with the documented
// metric names, labels and counters.
func TestMetricsEndpointPrometheus(t *testing.T) {
	core.DefaultMetrics.Reset()
	core.DefaultMetrics.ObserveLatency(core.SubsystemWebhook, 250*time.Millisecond)
	core.DefaultMetrics.IncOperations(core.SubsystemWebhook)
	core.DefaultMetrics.IncError(core.SubsystemWebhook)

	rec := httptest.NewRecorder()
	getMetrics(rec, httptest.NewRequest(http.MethodGet, "/api/metrics?format=prometheus", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE shhgit_latency_seconds gauge",
		`shhgit_latency_seconds{subsystem="webhook",quantile="0.5"}`,
		`shhgit_latency_seconds{subsystem="webhook",quantile="0.95"}`,
		`shhgit_latency_seconds{subsystem="webhook",quantile="0.99"}`,
		"# TYPE shhgit_latency_seconds_total counter",
		`shhgit_latency_seconds_total{subsystem="webhook"}`,
		`shhgit_operations_total{subsystem="webhook"} 1`,
		`shhgit_errors_total{subsystem="webhook"} 1`,
		"# TYPE shhgit_operations_total counter",
		"# TYPE shhgit_errors_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("prometheus output missing %q:\n%s", want, body)
		}
	}
}

// The prometheus rendering is deterministic: subsystem names are sorted.
func TestPrometheusMetricsSorted(t *testing.T) {
	core.DefaultMetrics.Reset()
	core.DefaultMetrics.IncOperations(core.SubsystemWebhook)
	core.DefaultMetrics.IncOperations(core.SubsystemScan)

	out := prometheusMetrics(core.DefaultMetrics.Snapshot())
	if strings.Index(out, `subsystem="scan"`) > strings.Index(out, `subsystem="webhook"`) {
		t.Fatalf("subsystems are not sorted:\n%s", out)
	}
}

// /api/metrics is registered behind localGuard like the rest of the API: an
// empty route table would make the dashboard's metrics card 404.
func TestMetricsRouteRegistered(t *testing.T) {
	prev := webBindIsLoopback
	webBindIsLoopback = true
	t.Cleanup(func() { webBindIsLoopback = prev })

	mux := http.NewServeMux()
	registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	// localGuard requires a loopback Host while the dashboard is bound locally;
	// httptest's default Host is "example.com".
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/metrics status = %d, want 200", rec.Code)
	}
}
