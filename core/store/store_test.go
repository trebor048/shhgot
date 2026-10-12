package store

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var testBase = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func testRecord(fp, source, signature string, ts time.Time) Record {
	return Record{
		ID:          fp + "-id",
		Fingerprint: fp,
		Timestamp:   ts,
		Source:      source,
		URL:         "https://example.test/" + fp,
		File:        "a/b.go",
		Signature:   signature,
		Matches:     []string{"first", "second"},
		Secret:      "secret-value",
		Line:        7,
		Stars:       11,
		Priority:    2,
	}
}

func newTestStore(t *testing.T, cfg Config) *FileStore {
	t.Helper()
	cfg.Enabled = true
	if cfg.Path == "" {
		cfg.Path = filepath.Join(t.TempDir(), "journal.jsonl")
	}
	st, err := newFileStore(cfg)
	if err != nil {
		t.Fatalf("newFileStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAppendReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	cfg := Config{Path: path, Enabled: true}

	st := newTestStore(t, cfg)
	r1 := testRecord("fp1", "github", "sig-a", testBase)
	r2 := testRecord("fp2", "gist", "sig-b", testBase.Add(time.Minute))
	if err := st.Append(r1); err != nil {
		t.Fatalf("append r1: %v", err)
	}
	if err := st.Append(r2); err != nil {
		t.Fatalf("append r2: %v", err)
	}
	if !st.Seen("fp1") || !st.Seen("fp2") {
		t.Fatal("appended fingerprints must be seen")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2 := newTestStore(t, cfg)
	got, err := st2.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 records, got %d", len(got))
	}
	if !reflect.DeepEqual(got[0], r1) || !reflect.DeepEqual(got[1], r2) {
		t.Fatalf("reloaded records differ:\n got %#v\nwant %#v", got, []Record{r1, r2})
	}
	if !st2.Seen("fp1") || !st2.Seen("fp2") {
		t.Fatal("reloaded fingerprints must be seen")
	}

	filtered, err := st2.List(Filter{Source: "gist"})
	if err != nil {
		t.Fatalf("list filter: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Fingerprint != "fp2" {
		t.Fatalf("source filter failed: %#v", filtered)
	}
	limited, err := st2.List(Filter{Limit: 1})
	if err != nil {
		t.Fatalf("list limit: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit failed: %d", len(limited))
	}
}

func TestTruncatedTailIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	good1, err := encodeFinding(testRecord("a", "src", "sig", testBase))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	good2, err := encodeFinding(testRecord("b", "src", "sig", testBase.Add(time.Hour)))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	partial := []byte(`{"t":"finding","rec":{"id":"c"`)
	data := append(append(append([]byte{}, good1...), good2...), partial...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	st := newTestStore(t, Config{Path: path, Enabled: true})
	got, err := st.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 complete records, got %d", len(got))
	}
	if !st.Seen("a") || !st.Seen("b") || st.Seen("c") {
		t.Fatalf("unexpected dedupe index: %#v", got)
	}
}

// A crash-truncated tail must be cut back before the journal is reopened for
// append. Otherwise the next flush concatenates a record onto the fragment,
// producing one invalid line that silently drops the new record on reload.
func TestTruncatedTailIsRepairedBeforeAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	good, err := encodeFinding(testRecord("a", "src", "sig", testBase))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	partial := []byte(`{"t":"finding","rec":{"id":"c"`)
	if err := os.WriteFile(path, append(append([]byte{}, good...), partial...), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	st := newTestStore(t, Config{Path: path, Enabled: true})
	if err := st.Append(testRecord("b", "src", "sig", testBase.Add(time.Hour))); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopening must observe both the original complete record and the record
	// appended after the truncated fragment was removed.
	st2 := newTestStore(t, Config{Path: path, Enabled: true})
	got, err := st2.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 records after appending onto a truncated tail, got %d", len(got))
	}
	if !st2.Seen("a") || !st2.Seen("b") {
		t.Fatalf("expected both records present, got %#v", got)
	}
}

// A sibling directory whose name merely begins with two dots ("..data") does
// not escape the base; only ".."/"../x" do. The old HasPrefix(rel, "..") test
// rejected the former.
func TestIsOutsideBase(t *testing.T) {
	sep := string(filepath.Separator)
	cases := []struct {
		rel  string
		want bool
	}{
		{".", false},
		{"journal.jsonl", false},
		{"..data" + sep + "journal.jsonl", false},
		{"sub" + sep + "..data", false},
		{"..", true},
		{".." + sep + "etc", true},
		{".." + sep + ".." + sep + "etc", true},
	}
	for _, c := range cases {
		if got := isOutsideBase(c.rel); got != c.want {
			t.Errorf("isOutsideBase(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// TestLegacyRecordWithoutTimestampDoesNotSuppress reproduces the legacy-record
// dedupe bug: a journal record whose Timestamp is zero used to have its index
// timestamp reset to now() on every load, so it never aged out of the dedupe TTL
// and kept suppressing the finding forever. Such a record must expire normally.
func TestLegacyRecordWithoutTimestampDoesNotSuppress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	line, err := encodeFinding(testRecord("legacy", "src", "sig", time.Time{}))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(path, line, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	now := testBase.Add(10 * time.Hour)
	cfg := Config{
		Path:      path,
		Enabled:   true,
		DedupeTTL: time.Hour,
		Now:       func() time.Time { return now },
	}
	st := newTestStore(t, cfg)
	if st.Seen("legacy") {
		t.Fatal("a record with no timestamp must not suppress a finding")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A missing timestamp must not resurrect the entry on the next load.
	st2 := newTestStore(t, cfg)
	if st2.Seen("legacy") {
		t.Fatal("a record with no timestamp must stay expired across reloads")
	}
}

func TestCompactionDedupes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	cfg := Config{Path: path, Enabled: true, CompactAfter: 3}
	st := newTestStore(t, cfg)

	if err := st.Append(testRecord("a", "src", "sig", testBase)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.Append(testRecord("b", "src", "sig", testBase.Add(time.Minute))); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.Append(testRecord("a", "src", "sig", testBase.Add(2*time.Minute))); err != nil {
		t.Fatalf("append: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n := bytes.Count(data, []byte{'\n'}); n != 2 {
		t.Fatalf("want 2 journal lines after compaction, got %d (%s)", n, data)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	st2 := newTestStore(t, cfg)
	got, err := st2.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 deduped records, got %d", len(got))
	}
	counts := map[string]int{}
	for _, r := range got {
		counts[r.Fingerprint]++
		if r.Fingerprint == "a" && !r.Timestamp.Equal(testBase.Add(2*time.Minute)) {
			t.Fatalf("latest duplicate not retained: %v", r.Timestamp)
		}
	}
	if counts["a"] != 1 || counts["b"] != 1 {
		t.Fatalf("duplicates not collapsed: %#v", counts)
	}
}

func TestDedupeTTLEviction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	now := testBase.Add(10 * time.Hour)
	cfg := Config{
		Path:      path,
		Enabled:   true,
		DedupeTTL: time.Hour,
		Now:       func() time.Time { return now },
	}
	st := newTestStore(t, cfg)
	if err := st.Append(testRecord("old", "src", "sig", now.Add(-2*time.Hour))); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.Append(testRecord("fresh", "src", "sig", now.Add(-time.Minute))); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2 := newTestStore(t, cfg)
	if st2.Seen("old") {
		t.Fatal("entry older than TTL must be evicted")
	}
	if !st2.Seen("fresh") {
		t.Fatal("entry within TTL must be retained")
	}
}

func TestDedupeMaxEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	cfg := Config{Path: path, Enabled: true, MaxDedupeEntries: 2}
	st := newTestStore(t, cfg)
	for i := 0; i < 5; i++ {
		fp := fmt.Sprintf("fp%d", i)
		if err := st.Append(testRecord(fp, "src", "sig", testBase.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2 := newTestStore(t, cfg)
	for _, fp := range []string{"fp0", "fp1", "fp2"} {
		if st2.Seen(fp) {
			t.Fatalf("entry %q should have been evicted", fp)
		}
	}
	for _, fp := range []string{"fp3", "fp4"} {
		if !st2.Seen(fp) {
			t.Fatalf("newest entry %q should be retained", fp)
		}
	}
}

// TestDedupeIndexHysteresis proves the dedupe index is not re-sorted on every
// append once it is at the cap: it grows to the cap-plus-quarter high-water mark
// before trimming, and Seen still reflects the retained window.
func TestDedupeIndexHysteresis(t *testing.T) {
	const max = 50
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	st := newTestStore(t, Config{Path: path, Enabled: true, MaxDedupeEntries: max})

	high := max + max/4
	overCap := false
	const appends = 500
	for i := 0; i < appends; i++ {
		fp := fmt.Sprintf("fp%d", i)
		if err := st.Append(testRecord(fp, "src", "sig", testBase.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		st.mu.Lock()
		n := len(st.index)
		st.mu.Unlock()
		if n > high {
			t.Fatalf("index grew to %d, above the hysteresis bound %d", n, high)
		}
		if n > max {
			overCap = true
		}
	}
	if !overCap {
		t.Fatalf("index never exceeded the cap %d; trimming was not deferred", max)
	}
	if !st.Seen(fmt.Sprintf("fp%d", appends-1)) {
		t.Fatal("the newest fingerprint must still be seen")
	}
	if st.Seen("fp0") {
		t.Fatal("the oldest fingerprint must have been trimmed away")
	}
}

// TestCompactTrimsDedupeIndex proves compaction trims the index against the
// current cap instead of waiting for the next append to grow past the mark.
func TestCompactTrimsDedupeIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	st := newTestStore(t, Config{Path: path, Enabled: true, MaxDedupeEntries: 1000})
	for i := 0; i < 5; i++ {
		fp := fmt.Sprintf("fp%d", i)
		if err := st.Append(testRecord(fp, "src", "sig", testBase.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	st.mu.Lock()
	st.cfg.MaxDedupeEntries = 2
	st.mu.Unlock()

	if err := st.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !st.Seen("fp4") {
		t.Fatal("the newest fingerprint must survive compaction")
	}
	if st.Seen("fp0") {
		t.Fatal("index was not trimmed during compaction")
	}
	st.mu.Lock()
	n := len(st.index)
	st.mu.Unlock()
	if n > 2 {
		t.Fatalf("index size %d after compaction, want <= 2", n)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	cfg := Config{Path: path, Enabled: true}

	type eventsCursor struct {
		Page int
		ETag string
	}
	want := eventsCursor{Page: 3, ETag: "abc"}
	wantBlob := []byte{1, 2, 3, 4}

	st := newTestStore(t, cfg)
	if err := st.SaveCursor("events", want); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	if err := st.SaveCursor("seen", wantBlob); err != nil {
		t.Fatalf("save blob: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2 := newTestStore(t, cfg)
	var got eventsCursor
	if err := st2.LoadCursor("events", &got); err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if got != want {
		t.Fatalf("cursor mismatch: got %#v want %#v", got, want)
	}
	var gotBlob []byte
	if err := st2.LoadCursor("seen", &gotBlob); err != nil {
		t.Fatalf("load blob: %v", err)
	}
	if !bytes.Equal(gotBlob, wantBlob) {
		t.Fatalf("blob mismatch: got %v want %v", gotBlob, wantBlob)
	}
	var missing eventsCursor
	if err := st2.LoadCursor("missing", &missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestExportCSVRoundTrip(t *testing.T) {
	recs := []Record{
		testRecord("fp,1", "src", "sig", testBase),
		testRecord("fp2", "other", "sig2", testBase.Add(time.Hour)),
	}
	recs[0].Secret = "a,\"b\"\nline"
	recs[0].Matches = []string{"x,y", "z\"q"}

	var buf bytes.Buffer
	if err := ExportCSV(&buf, recs); err != nil {
		t.Fatalf("export csv: %v", err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if len(rows) != len(recs)+1 {
		t.Fatalf("want %d csv rows, got %d", len(recs)+1, len(rows))
	}
	if !reflect.DeepEqual(rows[0], csvHeader) {
		t.Fatalf("header mismatch: %#v", rows[0])
	}
	for i, rec := range recs {
		got, err := recordFromCSV(rows[i+1])
		if err != nil {
			t.Fatalf("parse row %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, rec) {
			t.Fatalf("csv row %d mismatch:\n got %#v\nwant %#v", i, got, rec)
		}
	}
}

func recordFromCSV(row []string) (Record, error) {
	if len(row) != len(csvHeader) {
		return Record{}, fmt.Errorf("unexpected column count %d", len(row))
	}
	ts, err := time.Parse(time.RFC3339Nano, row[2])
	if err != nil {
		return Record{}, err
	}
	var matches []string
	if err := json.Unmarshal([]byte(row[7]), &matches); err != nil {
		return Record{}, err
	}
	line, err := strconv.Atoi(row[9])
	if err != nil {
		return Record{}, err
	}
	stars, err := strconv.Atoi(row[10])
	if err != nil {
		return Record{}, err
	}
	priority, err := strconv.Atoi(row[11])
	if err != nil {
		return Record{}, err
	}
	return Record{
		ID:          row[0],
		Fingerprint: row[1],
		Timestamp:   ts,
		Source:      row[3],
		URL:         row[4],
		File:        row[5],
		Signature:   row[6],
		Matches:     matches,
		Secret:      row[8],
		Line:        line,
		Stars:       stars,
		Priority:    priority,
	}, nil
}

func TestExportJSONLRoundTrip(t *testing.T) {
	recs := []Record{
		testRecord("fp1", "src", "sig", testBase),
		testRecord("fp2", "other", "sig2", testBase.Add(time.Hour)),
	}
	recs[1].Secret = ""
	recs[1].Line = 0

	var buf bytes.Buffer
	if err := ExportJSONL(&buf, recs); err != nil {
		t.Fatalf("export jsonl: %v", err)
	}
	trimmed := strings.TrimRight(buf.String(), "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) != len(recs) {
		t.Fatalf("want %d lines, got %d", len(recs), len(lines))
	}
	var got []Record
	for i, line := range lines {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("parse line %d: %v", i, err)
		}
		got = append(got, r)
	}
	if !reflect.DeepEqual(got, recs) {
		t.Fatalf("jsonl round trip mismatch:\n got %#v\nwant %#v", got, recs)
	}
}

func TestEncryptedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.enc")
	recs := []Record{
		testRecord("fp1", "src", "sig", testBase),
		testRecord("fp2", "other", "sig2", testBase.Add(time.Hour)),
	}
	const passphrase = "correct horse battery staple"

	if err := ExportEncrypted(path, recs, passphrase); err != nil {
		t.Fatalf("export encrypted: %v", err)
	}
	got, err := DecryptFile(path, passphrase)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !reflect.DeepEqual(got, recs) {
		t.Fatalf("decrypted records differ:\n got %#v\nwant %#v", got, recs)
	}

	wrong, err := DecryptFile(path, "not the passphrase")
	if err == nil {
		t.Fatal("wrong passphrase must fail")
	}
	if len(wrong) != 0 {
		t.Fatalf("wrong passphrase must not emit records, got %d", len(wrong))
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	blob[len(blob)-1] ^= 0xFF
	if err := os.WriteFile(path, blob, 0600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	if _, err := DecryptFile(path, passphrase); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
}

func TestWriteFailureDegraded(t *testing.T) {
	st := newTestStore(t, Config{Path: filepath.Join(t.TempDir(), "journal.jsonl"), Enabled: true})
	sentinel := errors.New("simulated disk failure")
	st.mu.Lock()
	st.writeFn = func([]byte) (int, error) { return 0, sentinel }
	st.mu.Unlock()

	err := st.Append(testRecord("fp1", "src", "sig", testBase))
	if err == nil {
		t.Fatal("expected append error on write failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
	if !st.Degraded() {
		t.Fatal("store must report degraded after write failure")
	}

	got, err := st.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("reads must keep working while degraded, got %d records", len(got))
	}
	if err := st.Append(testRecord("fp2", "src", "sig", testBase)); err == nil {
		t.Fatal("appends must keep failing while degraded")
	}
}

// TestShortWriteKeepsUnwrittenSuffix proves a partial write does not discard
// the unwritten bytes: the store degrades, retains the remainder, and a later
// flush completes the journal without re-appending the written prefix.
func TestShortWriteKeepsUnwrittenSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	st := newTestStore(t, Config{Path: path, Enabled: true})

	line, err := encodeFinding(testRecord("fp1", "src", "sig-a", testBase))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	cut := len(line) / 2
	var (
		written bytes.Buffer
		calls   int
	)
	st.mu.Lock()
	st.writeFn = func(p []byte) (int, error) {
		calls++
		if calls == 1 {
			written.Write(p[:cut])
			return cut, nil
		}
		written.Write(p)
		return len(p), nil
	}
	st.mu.Unlock()

	if err := st.Append(testRecord("fp1", "src", "sig-a", testBase)); err == nil {
		t.Fatal("a short write must be reported as an error")
	}
	if !st.Degraded() {
		t.Fatal("store must degrade after a short write")
	}
	st.mu.Lock()
	retained := append([]byte(nil), st.pending...)
	st.mu.Unlock()
	if !bytes.Equal(retained, line[cut:]) {
		t.Fatalf("unwritten suffix lost: got %q want %q", retained, line[cut:])
	}

	st.mu.Lock()
	flushErr := st.flushLocked()
	st.mu.Unlock()
	if flushErr != nil {
		t.Fatalf("completing flush: %v", flushErr)
	}
	if !bytes.Equal(written.Bytes(), line) {
		t.Fatalf("journal bytes corrupted:\n got %q\nwant %q", written.Bytes(), line)
	}

	reload := filepath.Join(t.TempDir(), "reload.jsonl")
	if err := os.WriteFile(reload, written.Bytes(), 0600); err != nil {
		t.Fatalf("write reload: %v", err)
	}
	st2 := newTestStore(t, Config{Path: reload, Enabled: true})
	got, err := st2.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Fingerprint != "fp1" {
		t.Fatalf("reloaded journal not intact: %#v", got)
	}
}

func TestConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	const goroutines, perGoroutine = 8, 25

	st := newTestStore(t, Config{Path: path, Enabled: true})
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				fp := fmt.Sprintf("g%d-%d", g, i)
				if err := st.Append(testRecord(fp, "src", "sig", testBase)); err != nil {
					t.Errorf("append %s: %v", fp, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2 := newTestStore(t, Config{Path: path, Enabled: true})
	got, err := st2.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != goroutines*perGoroutine {
		t.Fatalf("want %d records, got %d", goroutines*perGoroutine, len(got))
	}
}

func TestDisabledStore(t *testing.T) {
	st, err := New(Config{Path: filepath.Join(t.TempDir(), "journal.jsonl")})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if st.Degraded() {
		t.Fatal("disabled store must not be degraded")
	}
	if st.Seen("anything") {
		t.Fatal("disabled store must not dedupe")
	}
	if err := st.Append(testRecord("fp", "src", "sig", testBase)); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := st.List(Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("disabled store must not retain records, got %d", len(got))
	}
	if err := st.SaveCursor("c", 5); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	var v int
	if err := st.LoadCursor("c", &v); err != nil || v != 5 {
		t.Fatalf("cursor round trip in disabled store: %v %d", err, v)
	}
	if err := st.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestFingerprintStable(t *testing.T) {
	a := Fingerprint("sig", "src", "match", "repo", "file")
	b := Fingerprint("sig", "src", "match", "repo", "file")
	if a != b {
		t.Fatal("fingerprint must be deterministic")
	}
	if len(a) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(a))
	}
	if Fingerprint("sig2", "src", "match", "repo", "file") == a {
		t.Fatal("fingerprint must depend on its inputs")
	}
}

// TestFingerprintDistinguishesFieldBoundaries proves concatenating adjacent
// fields without a separator does not let shifted values collide.
func TestFingerprintDistinguishesFieldBoundaries(t *testing.T) {
	a := Fingerprint("sig", "src", "match", ".../a", "b.py")
	b := Fingerprint("sig", "src", "match", ".../ab", ".py")
	if a == b {
		t.Fatal("boundary-shifted inputs must not collide")
	}
}
