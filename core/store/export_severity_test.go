package store

import (
	"bytes"
	"encoding/csv"
	"testing"
)

// TestExportCSVCarriesSeverityColumns proves the Slice 6 columns are present in
// the header, populated from the Record when set, and empty (not absent) for a
// record that predates the feature.
func TestExportCSVCarriesSeverityColumns(t *testing.T) {
	rec := testRecord("fp1", "github", "OpenAI API Key", testBase)
	rec.Severity = "critical"
	rec.Provider = "OpenAI"
	rec.Tier = "GPT-4"
	rec.Balance = "$150.00"

	plain := testRecord("fp2", "gist", "sig", testBase)

	var buf bytes.Buffer
	if err := ExportCSV(&buf, []Record{rec, plain}); err != nil {
		t.Fatalf("export csv: %v", err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want header + 2 rows, got %d", len(rows))
	}
	if len(rows[0]) != len(csvHeader) {
		t.Fatalf("header has %d columns, want %d", len(rows[0]), len(csvHeader))
	}

	index := make(map[string]int, len(rows[0]))
	for i, name := range rows[0] {
		index[name] = i
	}
	for _, col := range []string{"severity", "provider", "tier", "balance"} {
		if _, ok := index[col]; !ok {
			t.Fatalf("csv header is missing the %q column: %v", col, rows[0])
		}
	}

	got := rows[1]
	checks := map[string]string{
		"severity": "critical",
		"provider": "OpenAI",
		"tier":     "GPT-4",
		"balance":  "$150.00",
	}
	for col, want := range checks {
		if got[index[col]] != want {
			t.Errorf("row[%s] = %q, want %q", col, got[index[col]], want)
		}
	}

	// A record written before the feature keeps the columns but leaves them
	// empty, which is the documented contract.
	for _, col := range []string{"severity", "provider", "tier", "balance"} {
		if v := rows[2][index[col]]; v != "" {
			t.Errorf("plain record %s = %q, want empty", col, v)
		}
	}
}
