package core

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MatchLogger writes each detected match to a per-signature-name CSV file.
// Files are append-only across runs (never overwritten) and the open handles
// are cached so a scan does not re-open the same file on every match.
// All state access is guarded by the embedded mutex because matches arrive
// from multiple goroutines.
type MatchLogger struct {
	sync.Mutex
	dir     string
	enabled bool
	files   map[string]*csvWriter // full path -> CSV writer wrapper
}

// csvWriter wraps a file and CSV writer together with tracking for first write
type csvWriter struct {
	file   *os.File
	writer *csv.Writer
	isNew  bool // true if this file was just created (needs header)
}

// NewMatchLogger creates a MatchLogger that writes into dir. When enabled is
// false, LogMatch is a no-op.
func NewMatchLogger(dir string, enabled bool) *MatchLogger {
	return &MatchLogger{
		dir:     dir,
		enabled: enabled,
		files:   make(map[string]*csvWriter),
	}
}

// LogMatch appends a single CSV row describing one detected match to the file
// derived from signatureName. It returns immediately when the logger is
// disabled. The open handle is cached, so the file is only opened once.
// CSV columns: Timestamp, Repository URL, File Path, Match Count, Matches
func (ml *MatchLogger) LogMatch(signatureName string, url string, fileName string, matches []string) {
	if ml == nil || !ml.enabled {
		return
	}

	safeName := sanitizeFileName(signatureName)
	fullPath := filepath.Join(ml.dir, safeName)

	ml.Lock()
	defer ml.Unlock()

	cw, ok := ml.files[fullPath]
	if !ok {
		if err := os.MkdirAll(ml.dir, 0755); err != nil {
			return
		}
		f, err := os.OpenFile(fullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return
		}

		// Check if file is new (empty) to determine if we need to write header
		fileInfo, err := f.Stat()
		isNew := err == nil && fileInfo.Size() == 0

		cw = &csvWriter{
			file:   f,
			writer: csv.NewWriter(f),
			isNew:  isNew,
		}

		// Write header if this is a new file
		if isNew {
			_ = cw.writer.Write([]string{"Timestamp", "Repository URL", "File Path", "Match Count", "Matches"})
		}

		ml.files[fullPath] = cw
	}

	// Sanitize values
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	cleanURL := sanitizeValue(url)
	cleanFile := sanitizeValue(fileName)

	cleanMatches := make([]string, 0, len(matches))
	for _, m := range matches {
		cleanMatches = append(cleanMatches, sanitizeValue(m))
	}

	// Build CSV row
	row := []string{
		timestamp,
		cleanURL,
		cleanFile,
		fmt.Sprintf("%d", len(cleanMatches)),
		strings.Join(cleanMatches, ", "),
	}

	// Write row (best-effort; a logging failure must never abort the scan)
	_ = cw.writer.Write(row)
	cw.writer.Flush()
}

// Close flushes and closes every cached handle and clears the map. It is safe
// to call multiple times.
func (ml *MatchLogger) Close() {
	if ml == nil {
		return
	}

	ml.Lock()
	defer ml.Unlock()

	for _, cw := range ml.files {
		cw.writer.Flush()
		_ = cw.file.Sync()
		_ = cw.file.Close()
	}
	ml.files = make(map[string]*csvWriter)
}

// sanitizeFileName turns a signature name into a safe, readable filename with
// a .csv extension (e.g. "OpenAI API Key" -> "OpenAI API Key.csv").
func sanitizeFileName(name string) string {
	// Replace control characters and characters that are invalid in Windows
	// filenames with an underscore.
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)

	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		`"`, "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	name = replacer.Replace(name)

	// Trim surrounding whitespace and collapse runs of spaces.
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "misc.csv"
	}
	return name + ".csv"
}

// sanitizeValue strips control characters from a value so each log entry stays
// on a single line.
func sanitizeValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
