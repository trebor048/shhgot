package core

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A matched value comes from the repository being scanned, so it may be hostile.
// A spreadsheet treats a cell beginning with =, +, - or @ as a formula and runs
// it when the operator opens the export, which turns "review the findings" into
// code execution on the reviewer's machine.
func TestCSVExportNeutralisesFormulas(t *testing.T) {
	dir := t.TempDir()
	ml := NewMatchLogger(dir, true)
	defer ml.Close()

	hostile := []string{
		`=cmd|'/c calc'!A0`,
		`+1+1`,
		`-2+3`,
		`@SUM(A1:A9)`,
	}
	ml.LogMatch("AWS Access Key", "https://github.com/acme/app", "config.go", hostile)

	ml.Close() // flush the handles before reading

	path := filepath.Join(dir, "AWS Access Key.csv")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}

	records, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("rows = %d, want a header and one match row", len(records))
	}
	matchesCell := records[1][4]
	for _, raw := range hostile {
		if !strings.Contains(matchesCell, raw) {
			t.Errorf("export lost the value %q: %q", raw, matchesCell)
		}
	}
	if !strings.HasPrefix(matchesCell, "'") {
		t.Errorf("matches cell = %q, want it to start with the text marker so no spreadsheet evaluates it", matchesCell)
	}

	// A URL cannot start a formula either, and an ordinary value must be left
	// exactly as it was found.
	ml2 := NewMatchLogger(t.TempDir(), true)
	defer ml2.Close()
	ml2.LogMatch("Plain", "https://github.com/acme/app", "main.go", []string{"AKIAIOSFODNN7EXAMPLE"})
	ml2.Close()

	plain, err := os.ReadFile(filepath.Join(ml2.dir, "Plain.csv"))
	if err != nil {
		t.Fatalf("read plain export: %v", err)
	}
	if !strings.Contains(string(plain), "AKIAIOSFODNN7EXAMPLE") {
		t.Error("an ordinary value was altered")
	}
	if strings.Contains(string(plain), "'AKIA") {
		t.Error("an ordinary value was needlessly quoted")
	}
}

func TestCSVCellLeavesSafeValuesAlone(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"AKIA123":     "AKIA123",
		"sk-live-abc": "sk-live-abc", // a dash inside the value is harmless
		"=1+1":        "'=1+1",
		"+1":          "'+1",
		"-1":          "'-1",
		"@x":          "'@x",
		"\tx":         "'\tx",
	}
	for in, want := range cases {
		if got := csvCell(in); got != want {
			t.Errorf("csvCell(%q) = %q, want %q", in, got, want)
		}
	}
}

// Logging must stay best effort: a broken destination must not abort a scan.
func TestMatchLoggerSurvivesAnUnwritableDestination(t *testing.T) {
	disabled := NewMatchLogger(t.TempDir(), false)
	disabled.LogMatch("Any", "u", "f", []string{"v"})
	disabled.Close()

	// A path that cannot be a directory: the call must return without panicking.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	ml := NewMatchLogger(blocked, true)
	defer ml.Close()
	ml.LogMatch("Any", "u", "f", []string{"v"})
}
