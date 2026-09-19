package core

import (
	"io/ioutil"
	"path/filepath"
	"testing"
	"time"
)

// intPtr returns a pointer to n (fresh allocation per call).
func intPtr(n int) *int { return &n }

// strPtr returns a pointer to s (fresh allocation per call).
func strPtr(s string) *string { return &s }

// TestParseConfigPerformanceSection verifies the "performance" section of
// config.yaml actually lands in the Config struct (it used to be silently
// ignored) and that absent keys fall back to the provided defaults.
func TestParseConfigPerformanceSection(t *testing.T) {
	dir := t.TempDir()
	cfg := `
performance:
  max_repository_threads: 20
  max_gist_threads: 10
  max_comment_threads: 10
  api_pages_per_cycle: 10
  api_per_page: 100
  api_sleep_seconds: 1
  worker_pool_size: 100
  queue_buffer_size: 10000
github_access_tokens:
  - 'ghp_placeholder'
`
	if err := ioutil.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	threads := 4
	opts := &Options{ConfigPath: &dir, Threads: &threads, Local: strPtr("")}
	config, err := ParseConfig(opts)
	if err != nil {
		t.Fatal(err)
	}

	p := config.Performance
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"max_repository_threads", p.Int(p.MaxRepositoryThreads, 0), 20},
		{"max_gist_threads", p.Int(p.MaxGistThreads, 0), 10},
		{"max_comment_threads", p.Int(p.MaxCommentThreads, 0), 10},
		{"api_pages_per_cycle", p.Int(p.APIPagesPerCycle, 0), 10},
		{"api_per_page", p.Int(p.APIPerPage, 0), 100},
		{"api_sleep_seconds", p.Int(p.APISleepSeconds, 0), 1},
		{"worker_pool_size", p.Int(p.WorkerPoolSize, 0), 100},
		{"queue_buffer_size", p.Int(p.QueueBufferSize, 0), 10000},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	// Absent key must fall back to the caller-provided default.
	if got := p.Int(nil, 42); got != 42 {
		t.Errorf("absent key fallback = %d, want 42", got)
	}
	if got := p.Int(intPtr(-5), 42); got != 42 {
		t.Errorf("non-positive key fallback = %d, want 42", got)
	}
}

// TestPerfTuning verifies the event-loop tuning helper: compiled-in defaults
// when no config is present, and config values (with floors) when set.
func TestPerfTuning(t *testing.T) {
	// No config -> compiled-in defaults.
	perPage, sleep, maxPages, pool := perfTuning(nil)
	if perPage != defaultPerPage || sleep != defaultSleep || maxPages != defaultMaxPages || pool != defaultWorkerPool {
		t.Fatalf("defaults = (%d, %s, %d, %d), want (%d, %s, %d, %d)",
			perPage, sleep, maxPages, pool, defaultPerPage, defaultSleep, defaultMaxPages, defaultWorkerPool)
	}

	// Config values are honored.
	s := &Session{Config: &Config{Performance: PerformanceConfig{
		APIPerPage:      intPtr(50),
		APIPagesPerCycle: intPtr(7),
		WorkerPoolSize:  intPtr(64),
		APISleepSeconds: intPtr(2),
	}}}
	perPage, sleep, maxPages, pool = perfTuning(s)
	if perPage != 50 || sleep != 2*time.Second || maxPages != 7 || pool != 64 {
		t.Fatalf("tuned = (%d, %s, %d, %d), want (50, 2s, 7, 64)", perPage, sleep, maxPages, pool)
	}

	// Floors: per_page capped at 100, sleep floored at 1s, out-of-range clamps.
	s = &Session{Config: &Config{Performance: PerformanceConfig{
		APIPerPage:      intPtr(500),
		APIPagesPerCycle: intPtr(0),
		WorkerPoolSize:  intPtr(0),
		APISleepSeconds: intPtr(-3),
	}}}
	perPage, sleep, maxPages, pool = perfTuning(s)
	if perPage != 100 {
		t.Errorf("per_page clamp = %d, want 100", perPage)
	}
	if sleep != defaultSleep {
		t.Errorf("sleep with negative value = %s, want default %s", sleep, defaultSleep)
	}
	if maxPages != defaultMaxPages || pool != defaultWorkerPool {
		t.Errorf("zero pages/pool should fall back, got (%d, %d)", maxPages, pool)
	}
}
