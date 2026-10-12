package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	tagFinding = "finding"
	tagCursor  = "cursor"
	tagBlob    = "blob"

	flushInterval         = 250 * time.Millisecond
	defaultFlushThreshold = 32 << 10
)

var _ Store = (*FileStore)(nil)

// legacyIndexTimestamp is recorded in the dedupe index for a loaded finding
// record that carries no Timestamp. It is the Unix epoch rather than
// time.Time{} so it is older than any TTL and ages out immediately; using
// time.Now() here reset the entry to "now" on every load, so a timestamp-less
// record could suppress findings forever and every restart resurrected it.
var legacyIndexTimestamp = time.Unix(0, 0)

type envelope struct {
	T    string          `json:"t"`
	Rec  *Record         `json:"rec,omitempty"`
	Name string          `json:"name,omitempty"`
	V    json.RawMessage `json:"v,omitempty"`
}

type FileStore struct {
	// mu guards every field below. Read-only paths (List, Degraded) hold it
	// shared so dashboard/export reads do not stall scan appends; Seen keeps
	// the write lock because it evicts expired entries.
	mu  sync.RWMutex
	cfg Config
	now func() time.Time

	path string
	file *os.File

	pending []byte

	timer *time.Ticker
	done  chan struct{}
	wg    sync.WaitGroup

	records []Record
	cursors map[string]json.RawMessage
	index   map[string]time.Time

	degraded bool
	lastErr  error
	closed   bool

	closeOnce sync.Once
	closeErr  error

	appendsSinceCompact int

	writeFn        func([]byte) (int, error)
	flushThreshold int
}

// New opens the findings store. A path must have exactly one owning Store in
// the process that writes it: two FileStores on one journal interleave
// appends and share a compaction temp name, and load() truncates a torn tail.
func New(cfg Config) (Store, error) {
	if !cfg.Enabled {
		return newDisabledStore(), nil
	}
	return newFileStore(cfg)
}
func newFileStore(cfg Config) (*FileStore, error) {
	cfg = normalizeConfig(cfg)
	if cfg.Path == "" {
		return nil, errors.New("store: empty path")
	}
	absPath, err := resolveAndValidatePath(cfg.Path)
	if err != nil {
		return nil, err
	}
	s := &FileStore{
		cfg:            cfg,
		now:            cfg.Now,
		path:           absPath,
		cursors:        make(map[string]json.RawMessage),
		index:          make(map[string]time.Time),
		done:           make(chan struct{}),
		flushThreshold: defaultFlushThreshold,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			s.setDegradedLocked(err)
			s.start()
			return s, nil
		}
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		s.setDegradedLocked(err)
	} else {
		s.file = f
	}
	s.start()
	return s, nil
}

func (s *FileStore) start() {
	s.timer = time.NewTicker(flushInterval)
	s.wg.Add(1)
	go s.flushLoop()
}

// resolveAndValidatePath resolves path to an absolute path and ensures it resides
// within the user's home directory (or a configured base directory). This prevents
// path traversal via config like "../../../etc/cron.d/shhgit".
func resolveAndValidatePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("store: resolve path: %w", err)
	}
	// Restrict to user's home directory for safety. Operators can override by
	// setting STORE_BASE_DIR env var if they need a different location.
	baseDir := os.Getenv("STORE_BASE_DIR")
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("store: get home dir: %w", err)
		}
		baseDir = home
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("store: resolve base dir: %w", err)
	}
	// Ensure abs is within absBase
	rel, err := filepath.Rel(absBase, abs)
	if err != nil || isOutsideBase(rel) {
		return "", fmt.Errorf("store: path %q escapes allowed base directory %q", abs, absBase)
	}
	return abs, nil
}

// isOutsideBase reports whether rel, the result of filepath.Rel(base, target),
// escapes base. A plain strings.HasPrefix(rel, "..") test also rejects a
// legitimate entry whose name merely begins with two dots - `..data/journal`,
// `..cache` - which is not a traversal. Only the ".." element itself, or a path
// beginning ".." + separator, actually escapes.
func isOutsideBase(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func normalizeConfig(cfg Config) Config {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return cfg
}

func (s *FileStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	// Drop a crash-truncated trailing line. It is ignored when replaying below,
	// but the file is reopened O_APPEND, so leaving the fragment in place makes
	// the next flush concatenate a record onto it and turn the whole line into
	// invalid JSON - silently dropping the new record on the next load.
	if end := lastCompleteLineEnd(data); end != len(data) {
		if truncErr := os.Truncate(s.path, int64(end)); truncErr != nil {
			return truncErr
		}
		data = data[:end]
	}
	for _, line := range completeLines(data) {
		s.applyLine(line)
	}
	s.evictIndexLocked()
	s.trimIndexLocked()
	return nil
}

// lastCompleteLineEnd returns the length of the longest prefix of a journal that
// ends on a newline (zero when there is no newline at all). It is the offset a
// crash-truncated journal must be cut back to before the next append.
func lastCompleteLineEnd(data []byte) int {
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		return i + 1
	}
	return 0
}

func completeLines(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		out = append(out, data[:i])
		data = data[i+1:]
	}
	return out
}

func (s *FileStore) applyLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return
	}
	switch env.T {
	case tagFinding:
		if env.Rec == nil {
			return
		}
		rec := *env.Rec
		s.records = append(s.records, rec)
		if rec.Fingerprint != "" {
			ts := rec.Timestamp
			if ts.IsZero() {
				ts = legacyIndexTimestamp
			}
			s.index[rec.Fingerprint] = ts
		}
	case tagCursor, tagBlob:
		if env.Name == "" {
			return
		}
		s.cursors[env.Name] = append(json.RawMessage(nil), env.V...)
	}
}

func (s *FileStore) evictIndexLocked() {
	if s.cfg.DedupeTTL <= 0 {
		return
	}
	now := s.now()
	for fp, ts := range s.index {
		// A zero timestamp is old, not current: subtracting it from now() is
		// always greater than the TTL, so the entry expires rather than being
		// treated as freshly seen.
		if now.Sub(ts) > s.cfg.DedupeTTL {
			delete(s.index, fp)
		}
	}
}

// dedupeHighWater is the index size at which the dedupe index is trimmed back
// to MaxDedupeEntries. It is the cap plus a quarter, so the O(n log n) sort
// runs once per quarter-cap of growth instead of on every append, keeping the
// common append path O(1). A non-positive cap means "unbounded" and disables
// trimming.
func (s *FileStore) dedupeHighWater() int {
	max := s.cfg.MaxDedupeEntries
	if max <= 0 {
		return 0
	}
	// Saturate: max + max/4 overflows for a MaxDedupeEntries near MaxInt, which
	// would wrap the high-water mark negative and disable trimming entirely.
	if max > math.MaxInt-max/4 {
		return math.MaxInt
	}
	return max + max/4
}

func (s *FileStore) trimIndexLocked() {
	max := s.cfg.MaxDedupeEntries
	if max <= 0 || len(s.index) <= max {
		return
	}
	type entry struct {
		fp string
		ts time.Time
	}
	entries := make([]entry, 0, len(s.index))
	for fp, ts := range s.index {
		entries = append(entries, entry{fp: fp, ts: ts})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ts.After(entries[j].ts)
	})
	for _, e := range entries[max:] {
		delete(s.index, e.fp)
	}
}

func (s *FileStore) flushLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case <-s.timer.C:
			s.mu.Lock()
			if !s.closed {
				_ = s.flushLocked()
			}
			s.mu.Unlock()
		}
	}
}

func (s *FileStore) flushLocked() error {
	if len(s.pending) == 0 {
		return nil
	}
	var (
		n   int
		err error
	)
	switch {
	case s.writeFn != nil:
		n, err = s.writeFn(s.pending)
	case s.file != nil:
		n, err = s.file.Write(s.pending)
	default:
		err = errors.New("store: no writable journal")
	}
	if n < 0 {
		n = 0
	}
	if n > len(s.pending) {
		n = len(s.pending)
	}
	if n < len(s.pending) {
		// A short write means only the prefix landed. Keep the unwritten
		// suffix so the next flush appends exactly the remainder instead of
		// re-appending the whole buffer and corrupting the journal.
		s.pending = s.pending[n:]
		if err == nil {
			err = io.ErrShortWrite
		}
		s.setDegradedLocked(err)
		return err
	}
	s.pending = s.pending[:0]
	if err != nil {
		s.setDegradedLocked(err)
		return err
	}
	s.degraded = false
	s.lastErr = nil
	return nil
}

func (s *FileStore) maybeFlushLocked() error {
	if s.writeFn == nil && len(s.pending) < s.flushThreshold {
		return nil
	}
	return s.flushLocked()
}

func (s *FileStore) setDegradedLocked(err error) {
	s.degraded = true
	if err != nil {
		s.lastErr = err
	}
}

func (s *FileStore) Append(rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.degraded {
		return s.lastErr
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = s.now()
	}
	line, err := encodeFinding(rec)
	if err != nil {
		return err
	}
	s.records = append(s.records, rec)
	if rec.Fingerprint != "" {
		s.index[rec.Fingerprint] = rec.Timestamp
		if hw := s.dedupeHighWater(); hw > 0 && len(s.index) > hw {
			s.trimIndexLocked()
		}
	}
	s.pending = append(s.pending, line...)
	// The in-memory additions stay even when the flush below fails: a degraded
	// store keeps serving reads from memory (and retry-appends are refused by
	// the degraded gate above, so no duplicate can be introduced that way).
	if err := s.maybeFlushLocked(); err != nil {
		return err
	}
	s.appendsSinceCompact++
	if s.cfg.CompactAfter > 0 && s.appendsSinceCompact >= s.cfg.CompactAfter {
		return s.compactLocked()
	}
	return nil
}

func (s *FileStore) Seen(fp string) bool {
	if fp == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.index[fp]
	if !ok {
		return false
	}
	if s.cfg.DedupeTTL > 0 {
		if s.now().Sub(ts) > s.cfg.DedupeTTL {
			delete(s.index, fp)
			return false
		}
	}
	return true
}

func (s *FileStore) List(f Filter) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Record
	for _, r := range s.records {
		if f.Source != "" && r.Source != f.Source {
			continue
		}
		if f.Signature != "" && r.Signature != f.Signature {
			continue
		}
		if !f.Since.IsZero() && r.Timestamp.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && r.Timestamp.After(f.Until) {
			continue
		}
		// Copy the Matches slice: the struct copy alone still aliases the
		// store's backing array, so a caller mutating it would corrupt the
		// in-memory store (and the next compaction's output).
		r.Matches = append([]string(nil), r.Matches...)
		out = append(out, r)
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *FileStore) SaveCursor(name string, v any) error {
	if name == "" {
		return errors.New("store: empty cursor name")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	line, err := encodeKV(tagCursor, name, raw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.degraded {
		return s.lastErr
	}
	s.cursors[name] = raw
	s.pending = append(s.pending, line...)
	return s.flushLocked()
}

func (s *FileStore) LoadCursor(name string, out any) error {
	s.mu.Lock()
	raw, ok := s.cursors[name]
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if out == nil {
		return errors.New("store: nil cursor destination")
	}
	return json.Unmarshal(raw, out)
}

func (s *FileStore) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return s.compactLocked()
}

func (s *FileStore) compactLocked() error {
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		s.setDegradedLocked(err)
		return err
	}
	bw := bufio.NewWriter(f)
	deduped := dedupeFindings(s.records)
	for _, rec := range deduped {
		line, e := encodeFinding(rec)
		if e != nil {
			err = e
			break
		}
		if _, e = bw.Write(line); e != nil {
			err = e
			break
		}
	}
	if err == nil {
		for _, name := range sortedKeys(s.cursors) {
			line, e := encodeKV(tagCursor, name, s.cursors[name])
			if e != nil {
				err = e
				break
			}
			if _, e = bw.Write(line); e != nil {
				err = e
				break
			}
		}
	}
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		s.setDegradedLocked(err)
		return err
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	if err := os.Rename(tmp, s.path); err != nil {
		// Do not leak the temp file on a failed rename.
		_ = os.Remove(tmp)
		s.setDegradedLocked(err)
		if f2, e := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); e == nil {
			s.file = f2
		}
		return err
	}
	// Fsync the directory so the rename itself is durable; syncing only the
	// file can still lose the directory entry on power loss. Best-effort.
	syncDirForFile(s.path)
	f2, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		s.setDegradedLocked(err)
		return err
	}
	s.file = f2
	s.records = deduped
	// Compaction collapses duplicate records, so the dedupe index can also be
	// trimmed here (and honour a cap lowered since the last append) instead of
	// waiting for the next append to grow past the high-water mark.
	s.evictIndexLocked()
	s.trimIndexLocked()
	s.pending = s.pending[:0]
	s.appendsSinceCompact = 0
	s.degraded = false
	s.lastErr = nil
	return nil
}

func (s *FileStore) Close() error {
	// sync.Once makes a concurrent/second Close wait for the first to finish
	// and observe its result, instead of returning nil while the first is still
	// flushing and closing the file.
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		if s.timer != nil {
			s.timer.Stop()
		}
		close(s.done)
		s.wg.Wait()

		s.mu.Lock()
		defer s.mu.Unlock()
		err := s.flushLocked()
		if s.file != nil {
			// Sync so accepted-but-buffered writes survive a crash right after
			// a clean Close.
			if serr := s.file.Sync(); serr != nil && err == nil {
				err = serr
			}
			if cerr := s.file.Close(); cerr != nil && err == nil {
				err = cerr
			}
			s.file = nil
		}
		s.closeErr = err
	})
	return s.closeErr
}

func (s *FileStore) Degraded() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.degraded
}

func encodeFinding(rec Record) ([]byte, error) {
	line, err := json.Marshal(envelope{T: tagFinding, Rec: &rec})
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

func encodeKV(tag, name string, raw json.RawMessage) ([]byte, error) {
	line, err := json.Marshal(envelope{T: tag, Name: name, V: raw})
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

func dedupeFindings(recs []Record) []Record {
	last := make(map[string]int)
	for i, r := range recs {
		if r.Fingerprint != "" {
			last[r.Fingerprint] = i
		}
	}
	out := make([]Record, 0, len(recs))
	for i, r := range recs {
		if r.Fingerprint == "" {
			out = append(out, r)
			continue
		}
		if last[r.Fingerprint] == i {
			out = append(out, r)
		}
	}
	return out
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var _ Store = (*disabledStore)(nil)

type disabledStore struct {
	mu      sync.Mutex
	cursors map[string]json.RawMessage
}

func newDisabledStore() *disabledStore {
	return &disabledStore{cursors: make(map[string]json.RawMessage)}
}

func (d *disabledStore) Seen(string) bool { return false }

func (d *disabledStore) Append(Record) error { return nil }

func (d *disabledStore) List(Filter) ([]Record, error) { return nil, nil }

func (d *disabledStore) SaveCursor(name string, v any) error {
	if name == "" {
		return errors.New("store: empty cursor name")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.cursors[name] = raw
	d.mu.Unlock()
	return nil
}

func (d *disabledStore) LoadCursor(name string, out any) error {
	d.mu.Lock()
	raw, ok := d.cursors[name]
	d.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if out == nil {
		return errors.New("store: nil cursor destination")
	}
	return json.Unmarshal(raw, out)
}

func (d *disabledStore) Compact() error { return nil }

func (d *disabledStore) Close() error { return nil }

func (d *disabledStore) Degraded() bool { return false }
