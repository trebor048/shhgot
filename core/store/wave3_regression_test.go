package store

import (
	"bytes"
	"strings"
	"testing"
)

// TestExportCSVNeutralisesFormulaInjection verifies a field drawn from scanned
// content that begins with a spreadsheet formula character is prefixed so it
// cannot execute when the CSV is opened in Excel/Sheets (CWE-1236).
func TestExportCSVNeutralisesFormulaInjection(t *testing.T) {
	var buf bytes.Buffer
	recs := []Record{
		{ID: "r1", Secret: "=cmd|'/c calc'!A0", File: "+evil", URL: "@at"},
	}
	if err := ExportCSV(&buf, recs); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"'=cmd", "'+evil", "'@at"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ExportCSV output missing neutralised cell %q:\n%s", want, out)
		}
	}
}
