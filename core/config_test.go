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

// TestParseConfigScanningSection verifies the "scanning" section of config.yaml
// actually lands in the Config struct. yaml.Unmarshal drops keys with no
// matching field, so before ScanningConfig existed every key in this section —
// including max_file_count — was silently ignored.
func TestParseConfigScanningSection(t *testing.T) {
	dir := t.TempDir()
	cfg := `
scanning:
  max_file_size_mb: 10
  max_repo_size_mb: 1000
  max_file_count: 250
  clone_depth: 1
  clone_timeout_seconds: 60
  scan_timeout_seconds: 120
  skip_forks: true
  skip_archived: false
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

	s := config.Scanning
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"max_file_size_mb", s.Int(s.MaxFileSizeMB, 0), 10},
		{"max_repo_size_mb", s.Int(s.MaxRepoSizeMB, 0), 1000},
		{"max_file_count", s.Int(s.MaxFileCount, 0), 250},
		{"clone_depth", s.Int(s.CloneDepth, 0), 1},
		{"clone_timeout_seconds", s.Int(s.CloneTimeoutSecs, 0), 60},
		{"scan_timeout_seconds", s.Int(s.ScanTimeoutSecs, 0), 120},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	// An explicit false must survive as false (Bool cannot use the "> 0" trick).
	if got := s.Bool(s.SkipForks, false); got != true {
		t.Errorf("skip_forks = %v, want true", got)
	}
	if got := s.Bool(s.SkipArchived, true); got != false {
		t.Errorf("skip_archived = %v, want false", got)
	}

	// An absent key falls back to the caller-provided default.
	if got := s.Bool(nil, true); got != true {
		t.Errorf("absent bool fallback = %v, want true", got)
	}
	if got := s.Int(nil, 42); got != 42 {
		t.Errorf("absent int fallback = %d, want 42", got)
	}
	if got := s.Int(intPtr(-5), 42); got != 42 {
		t.Errorf("non-positive int fallback = %d, want 42", got)
	}
}

// TestConfigMaxFileCountPrecedence pins the resolution order for the
// per-repository file cap: scanning.max_file_count, then the legacy
// performance.max_file_count, then the caller default.
func TestConfigMaxFileCountPrecedence(t *testing.T) {
	c := &Config{}
	if got := c.MaxFileCount(10000); got != 10000 {
		t.Errorf("empty config = %d, want default 10000", got)
	}

	c = &Config{Performance: PerformanceConfig{MaxFileCount: intPtr(5000)}}
	if got := c.MaxFileCount(10000); got != 5000 {
		t.Errorf("legacy performance key = %d, want 5000", got)
	}

	c = &Config{
		Performance: PerformanceConfig{MaxFileCount: intPtr(5000)},
		Scanning:    ScanningConfig{MaxFileCount: intPtr(250)},
	}
	if got := c.MaxFileCount(10000); got != 250 {
		t.Errorf("scanning key should win = %d, want 250", got)
	}

	// A nil receiver must not panic (defensive: callers may hold a nil Config).
	var nilCfg *Config
	if got := nilCfg.MaxFileCount(10000); got != 10000 {
		t.Errorf("nil config = %d, want default 10000", got)
	}
}

// TestApplyScanningOverrides verifies the MB->KB conversion and that an absent
// key leaves the command-line value untouched.
func TestApplyScanningOverrides(t *testing.T) {
	// No scanning section: nothing changes.
	opts := &Options{
		MaximumFileSize:        uintPtr(256),
		MaximumRepositorySize:  uintPtr(5120),
		CloneRepositoryTimeout: uintPtr(30),
	}
	(&Config{}).ApplyScanningOverrides(opts)
	if *opts.MaximumFileSize != 256 || *opts.MaximumRepositorySize != 5120 || *opts.CloneRepositoryTimeout != 30 {
		t.Fatalf("absent section changed options: %d/%d/%d",
			*opts.MaximumFileSize, *opts.MaximumRepositorySize, *opts.CloneRepositoryTimeout)
	}

	opts = &Options{
		MaximumFileSize:        uintPtr(256),
		MaximumRepositorySize:  uintPtr(5120),
		CloneRepositoryTimeout: uintPtr(30),
	}
	(&Config{Scanning: ScanningConfig{
		MaxFileSizeMB:    intPtr(10),
		MaxRepoSizeMB:    intPtr(1000),
		CloneTimeoutSecs: intPtr(60),
	}}).ApplyScanningOverrides(opts)

	if *opts.MaximumFileSize != 10*1024 {
		t.Errorf("MaximumFileSize = %d KB, want %d", *opts.MaximumFileSize, 10*1024)
	}
	if *opts.MaximumRepositorySize != 1000*1024 {
		t.Errorf("MaximumRepositorySize = %d KB, want %d", *opts.MaximumRepositorySize, 1000*1024)
	}
	if *opts.CloneRepositoryTimeout != 60 {
		t.Errorf("CloneRepositoryTimeout = %d, want 60", *opts.CloneRepositoryTimeout)
	}
}

func uintPtr(n uint) *uint { return &n }

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
		APIPerPage:       intPtr(50),
		APIPagesPerCycle: intPtr(7),
		WorkerPoolSize:   intPtr(64),
		APISleepSeconds:  intPtr(2),
	}}}
	perPage, sleep, maxPages, pool = perfTuning(s)
	if perPage != 50 || sleep != 2*time.Second || maxPages != 7 || pool != 64 {
		t.Fatalf("tuned = (%d, %s, %d, %d), want (50, 2s, 7, 64)", perPage, sleep, maxPages, pool)
	}

	// Floors: per_page capped at 100, sleep floored at 1s, out-of-range clamps.
	s = &Session{Config: &Config{Performance: PerformanceConfig{
		APIPerPage:       intPtr(500),
		APIPagesPerCycle: intPtr(0),
		WorkerPoolSize:   intPtr(0),
		APISleepSeconds:  intPtr(-3),
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
