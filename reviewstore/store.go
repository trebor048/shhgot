// Package reviewstore provides durable, concurrency-safe storage for AI
// security review jobs, backed by plain JSON files on disk (no database).
//
// # Layout
//
// One review per file, at <dir>/<id>.json, written atomically: the encoded
// review goes to a temp file in the same directory, is flushed, and is then
// committed with os.Rename. Files are created with mode 0600 and the store
// directory with 0700, because a review contains the plaintext secret it was
// created for.
//
// # Concurrency model
//
// The store keeps an in-memory index of every review it has loaded.
//
//   - Reads (Get, List, Count) are served from that index under a
//     sync.RWMutex and never touch the disk.
//   - Mutations (Create, Update, AppendMessage, Delete) hold a single write
//     mutex across the whole read-modify-write-persist-commit cycle, so the
//     filesystem write for a given review can never interleave with another
//     mutation, and a read-modify-write (AppendMessage) cannot lose an update.
//     The index is only committed after the file write succeeds, so in-memory
//     state never runs ahead of what is on disk.
//
// Lock order is always Store.fileMu -> Store.mu and never the reverse, so no
// deadlock is possible.
//
// The single write mutex also serialises writes to *different* reviews. That
// is a deliberate trade-off: a review write is dominated by multi-second AI
// provider latency, so the extra serialisation around a few kilobytes of JSON
// is immovable next to the work being stored, while a per-ID lock map would
// add unbounded bookkeeping and extra failure modes for no measurable gain.
//
// # Error semantics
//
//   - Get returns (nil, false) when the id is unknown or invalid.
//   - Update, Delete and AppendMessage return an error wrapping
//     os.ErrNotExist for an unknown id, and treat an invalid id exactly like
//     an unknown one (so errors.Is(err, os.ErrNotExist) holds for both).
//   - Create returns an error wrapping os.ErrInvalid for an invalid or
//     caller-supplied id, and one wrapping os.ErrExist for a duplicate id.
//
// # Loading and mutation tolerance
//
// NewStore loads every <id>.json it can parse and silently skips anything it
// cannot: unparseable JSON, files with an invalid id in their name, and
// dot-prefixed temp files left behind by an interrupted write. List and Count
// therefore keep working over directories written by older versions of the
// store, and a corrupt file can never make a call fail.
//
// The in-memory index is a snapshot taken by NewStore. Reviews written into
// dir by some *other* process after the store was opened are not visible to
// Get, List or Count until the directory is reopened with NewStore, so a
// directory must have exactly one owning Store in the process that writes it.
package reviewstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is the lifecycle state of a review.
type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Message is one turn of the follow-up chat attached to a review.
type Message struct {
	Role      string    `json:"role"` // "user" or "assistant"
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// Review is one AI review of one detected secret.
type Review struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// What was reviewed.
	Signature string `json:"signature"`
	Secret    string `json:"secret"`
	File      string `json:"file"`
	URL       string `json:"url"`
	Repo      string `json:"repo"`
	Context   string `json:"context"` // surrounding file content / match context

	// How it was reviewed.
	Provider string `json:"provider"`
	Model    string `json:"model"`

	Status     Status    `json:"status"`
	Assessment string    `json:"assessment"` // the generated markdown report
	Error      string    `json:"error"`
	Messages   []Message `json:"messages"` // follow-up conversation
}

// Store is a durable, concurrency-safe collection of reviews backed by one
// JSON file per review inside a single directory. The zero value is not
// usable; create stores with NewStore.
type Store struct {
	dir string

	// mu guards index. It is never held across filesystem I/O.
	mu    sync.RWMutex
	index map[string]*Review

	// fileMu serialises every filesystem mutation and is held across each
	// mutation's whole read-modify-write-persist-commit cycle.
	fileMu sync.Mutex
}

const (
	// jsonExt is the suffix of every review file.
	jsonExt = ".json"
	// tmpPrefix marks in-flight temp files. They are dot-prefixed and never
	// carry the jsonExt suffix, so neither the loader nor a caller can ever
	// mistake a leftover temp file for a review.
	tmpPrefix = ".tmp-"
	// maxIDLen keeps ids well clear of the usual 255-byte file name limit.
	maxIDLen = 128
	// idTimeLayout makes generated ids lexicographically time-sortable: the
	// layout has no trailing-zero trimming (nine zeros, not nines), so it is a
	// fixed-width, zero-padded prefix.
	idTimeLayout = "20060102T150405.000000000"
	// idRandBytes is the entropy in the random suffix of a generated id.
	idRandBytes = 5
)

// NewStore creates (if needed) dir and returns a store rooted there.
//
// dir is created with mode 0700. Any review file already present is loaded
// into the in-memory index; files that cannot be parsed are ignored rather
// than reported, so a store always opens over a directory written by an older
// version or left half-written by a crash.
func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("reviewstore: empty store directory: %w", os.ErrInvalid)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("reviewstore: create store directory %q: %w", dir, err)
	}
	s := &Store{dir: dir, index: make(map[string]*Review)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load populates the index from dir. It is called once, before the store is
// shared, so it needs no locking.
func (s *Store) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("reviewstore: read store directory %q: %w", s.dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Dot-prefixed names are hidden files: our own temp files, and
		// anything else that is not a review.
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, jsonExt) {
			continue
		}
		id := strings.TrimSuffix(name, jsonExt)
		if validateID(id) != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			// Unreadable right now (locked, permissions); ignore instead of
			// failing the whole store.
			continue
		}
		var r Review
		if err := json.Unmarshal(data, &r); err != nil {
			// Corrupt, truncated or written by an older schema: skip it.
			continue
		}
		// The file name is authoritative for the id, so a review whose stored
		// id is empty or stale still lands in the right slot.
		r.ID = id
		s.index[id] = &r
	}
	return nil
}

// Create stores r, assigning an id and timestamps when they are empty. The
// assigned id and timestamps are written back into r, and r is never aliased:
// later mutations of r do not affect the stored review.
//
// It returns an error wrapping os.ErrInvalid if r carries an invalid id, and
// one wrapping os.ErrExist if that id is already stored.
func (s *Store) Create(r *Review) error {
	if r == nil {
		return errors.New("reviewstore: cannot create nil review")
	}

	id := r.ID
	if id == "" {
		generated, err := newID()
		if err != nil {
			return fmt.Errorf("reviewstore: generate review id: %w", err)
		}
		id = generated
	} else if err := validateID(id); err != nil {
		return fmt.Errorf("reviewstore: cannot create review: %w", err)
	}

	path, err := s.path(id)
	if err != nil {
		return fmt.Errorf("reviewstore: cannot create review: %w", err)
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	now := time.Now().UTC()
	next := *r
	next.ID = id
	if next.CreatedAt.IsZero() {
		next.CreatedAt = now
	}
	if next.UpdatedAt.IsZero() {
		next.UpdatedAt = next.CreatedAt
	}
	next.Messages = cloneMessages(r.Messages)

	s.mu.Lock()
	_, exists := s.index[id]
	s.mu.Unlock()
	if exists {
		return fmt.Errorf("reviewstore: review %q already exists: %w", id, os.ErrExist)
	}

	if err := writeAtomic(path, &next); err != nil {
		return err
	}

	s.mu.Lock()
	s.index[id] = &next
	s.mu.Unlock()

	r.ID = next.ID
	r.CreatedAt = next.CreatedAt
	r.UpdatedAt = next.UpdatedAt
	return nil
}

// Get returns a deep copy of the review with the given id. The bool reports
// whether the review exists; an unknown or invalid id yields (nil, false).
func (s *Store) Get(id string) (*Review, bool) {
	if validateID(id) != nil {
		return nil, false
	}
	s.mu.RLock()
	r, ok := s.index[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return clone(r), true
}

// List returns deep copies of every review, newest first (by CreatedAt, ties
// broken by id descending). The order is stable, and the returned slice and
// its messages are owned by the caller.
//
// List serves the index loaded by NewStore; it does not rescan the directory.
func (s *Store) List() []*Review {
	s.mu.RLock()
	out := make([]*Review, 0, len(s.index))
	for _, r := range s.index {
		out = append(out, clone(r))
	}
	s.mu.RUnlock()

	// Sorting copies outside the read lock keeps the lock hold time
	// proportional to the copy, not to O(n log n) comparisons.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Update replaces the stored review identified by r.ID with r, stamps
// UpdatedAt with the current time, persists it, and writes the stamped
// timestamps back into r. The id and CreatedAt of a stored review are
// immutable: r.ID selects the file and the stored CreatedAt is preserved.
//
// It returns os.ErrNotExist-compatible errors for an unknown or invalid id.
func (s *Store) Update(r *Review) error {
	if r == nil {
		return errors.New("reviewstore: cannot update nil review")
	}
	if err := validateID(r.ID); err != nil {
		return lookupErr(r.ID)
	}
	path, err := s.path(r.ID)
	if err != nil {
		return lookupErr(r.ID)
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	s.mu.RLock()
	cur, ok := s.index[r.ID]
	var createdAt time.Time
	if ok {
		createdAt = cur.CreatedAt
	}
	s.mu.RUnlock()
	if !ok {
		return lookupErr(r.ID)
	}

	now := time.Now().UTC()
	next := *r
	next.ID = r.ID
	next.CreatedAt = createdAt
	next.UpdatedAt = now
	next.Messages = cloneMessages(r.Messages)

	if err := writeAtomic(path, &next); err != nil {
		return err
	}

	s.mu.Lock()
	s.index[r.ID] = &next
	s.mu.Unlock()

	r.CreatedAt = next.CreatedAt
	r.UpdatedAt = next.UpdatedAt
	return nil
}

// AppendMessage appends one chat turn to the review with the given id, stamps
// the message's CreatedAt when it is empty, bumps the review's UpdatedAt,
// persists, and returns. It is safe to call while a review is StatusRunning,
// and concurrent appends to the same review are serialised so no turn is lost.
//
// It returns os.ErrNotExist-compatible errors for an unknown or invalid id.
func (s *Store) AppendMessage(id string, m Message) error {
	if err := validateID(id); err != nil {
		return lookupErr(id)
	}
	path, err := s.path(id)
	if err != nil {
		return lookupErr(id)
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	s.mu.RLock()
	cur, ok := s.index[id]
	var next Review
	if ok {
		next = *cur
		next.Messages = cloneMessages(cur.Messages)
	}
	s.mu.RUnlock()
	if !ok {
		return lookupErr(id)
	}

	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	next.Messages = append(next.Messages, m)
	next.UpdatedAt = now

	if err := writeAtomic(path, &next); err != nil {
		return err
	}

	s.mu.Lock()
	s.index[id] = &next
	s.mu.Unlock()
	return nil
}

// Delete removes the review with the given id from the index and deletes its
// file. A review whose file is already gone is still removed from the index
// successfully.
//
// It returns os.ErrNotExist-compatible errors for an unknown or invalid id.
func (s *Store) Delete(id string) error {
	if err := validateID(id); err != nil {
		return lookupErr(id)
	}
	path, err := s.path(id)
	if err != nil {
		return lookupErr(id)
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	s.mu.Lock()
	cur, ok := s.index[id]
	if ok {
		delete(s.index, id)
	}
	s.mu.Unlock()
	if !ok {
		return lookupErr(id)
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The file is the durable record: if it could not be removed, put the
		// review back so the index keeps matching the disk.
		s.mu.Lock()
		s.index[id] = cur
		s.mu.Unlock()
		return fmt.Errorf("reviewstore: delete review %q: %w", id, err)
	}
	return nil
}

// Count returns the number of reviews currently held.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.index)
}

// path returns the on-disk path of a review, rejecting ids that could resolve
// outside the store directory.
func (s *Store) path(id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	p := filepath.Join(s.dir, id+jsonExt)
	// Defense in depth: validateID already forbids separators, so the clean
	// parent of p is always the store directory.
	if filepath.Dir(p) != filepath.Clean(s.dir) {
		return "", fmt.Errorf("reviewstore: review id %q escapes the store directory: %w", id, os.ErrInvalid)
	}
	return p, nil
}

// newID returns a time-sortable, filesystem-safe id: a fixed-width UTC
// nanosecond timestamp plus a short random suffix that keeps ids unique when
// several are generated in the same instant.
func newID() (string, error) {
	var buf [idRandBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format(idTimeLayout) + "-" + hex.EncodeToString(buf[:]), nil
}

// validateID reports whether id is a safe single path element that the store
// can turn into <dir>/<id>.json. It rejects the empty string, anything longer
// than maxIDLen, a leading dot (which also rejects "." and ".."), any
// character outside [A-Za-z0-9._-] — so no separators, no drive letters, no
// whitespace — and Windows reserved device names, which cannot be created in
// a directory even with an extension.
func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("reviewstore: empty review id: %w", os.ErrInvalid)
	}
	if len(id) > maxIDLen {
		return fmt.Errorf("reviewstore: review id of %d bytes exceeds %d: %w", len(id), maxIDLen, os.ErrInvalid)
	}
	if strings.HasPrefix(id, ".") {
		return fmt.Errorf("reviewstore: review id %q must not start with a dot: %w", id, os.ErrInvalid)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return fmt.Errorf("reviewstore: review id %q contains illegal character %q: %w", id, string(rune(c)), os.ErrInvalid)
		}
	}
	if isReservedName(id) {
		return fmt.Errorf("reviewstore: review id %q is a reserved file name: %w", id, os.ErrInvalid)
	}
	return nil
}

// isReservedName reports whether id collides with a Windows reserved device
// name (the check is case-insensitive and looks at the part before the first
// dot, because "NUL.json" is just as unusable as "NUL").
func isReservedName(id string) bool {
	base := id
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 {
		prefix := strings.ToUpper(base[:3])
		if (prefix == "COM" || prefix == "LPT") && base[3] >= '0' && base[3] <= '9' {
			return true
		}
	}
	return false
}

// lookupErr is the single error shape for "that review is not here": an
// invalid id is reported exactly like an unknown one, so callers only have to
// handle one case with errors.Is(err, os.ErrNotExist).
func lookupErr(id string) error {
	if err := validateID(id); err != nil {
		return fmt.Errorf("reviewstore: review id %q is invalid: %w", id, os.ErrNotExist)
	}
	return fmt.Errorf("reviewstore: review %q: %w", id, os.ErrNotExist)
}

// clone returns a deep copy of r, including its message slice, so callers can
// never mutate stored state by accident.
func clone(r *Review) *Review {
	if r == nil {
		return nil
	}
	c := *r
	c.Messages = cloneMessages(r.Messages)
	return &c
}

// cloneMessages copies in. Message holds only value fields, so a shallow copy
// of each element is a full deep copy.
func cloneMessages(in []Message) []Message {
	if in == nil {
		return nil
	}
	out := make([]Message, len(in))
	copy(out, in)
	return out
}

// writeAtomic encodes r and commits it to path atomically: a 0600 temp file
// in the same directory, flushed, then renamed over the destination. A failed
// write can therefore never leave a partially written review behind, and the
// destination is either the old review or the new one — never a mix.
func writeAtomic(path string, r *Review) (err error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("reviewstore: encode review %q: %w", r.ID, err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return fmt.Errorf("reviewstore: create temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()

	if _, werr := tmp.Write(data); werr != nil {
		tmp.Close()
		return fmt.Errorf("reviewstore: write %q: %w", filepath.Base(path), werr)
	}
	// Small files, high value: pay the fsync so a committed review survives a
	// power loss. It costs microseconds next to the review itself.
	if serr := tmp.Sync(); serr != nil {
		tmp.Close()
		return fmt.Errorf("reviewstore: sync %q: %w", filepath.Base(path), serr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("reviewstore: close %q: %w", filepath.Base(path), cerr)
	}
	if rerr := os.Rename(tmpName, path); rerr != nil {
		return fmt.Errorf("reviewstore: commit %q: %w", filepath.Base(path), rerr)
	}
	return nil
}
