package reviewstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestStore returns a store rooted in a fresh temp directory.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", dir, err)
	}
	return s, dir
}

// mustCreate creates r and fails the test on error.
func mustCreate(t *testing.T, s *Store, r *Review) *Review {
	t.Helper()
	if err := s.Create(r); err != nil {
		t.Fatalf("Create(%+v): %v", r, err)
	}
	return r
}

// reviewPath is the on-disk location a store uses for id.
func reviewPath(dir, id string) string {
	return filepath.Join(dir, id+".json")
}

// jsonFiles returns the names of the review files in dir.
func jsonFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestCreateGetRoundTrip(t *testing.T) {
	s, dir := newTestStore(t)

	r := &Review{
		Signature: "aws-access-key",
		Secret:    "AKIAIOSFODNN7EXAMPLE",
		File:      "internal/config/config.go",
		URL:       "https://github.com/acme/app/blob/main/internal/config/config.go#L42",
		Repo:      "acme/app",
		Context:   "awsKey := \"AKIAIOSFODNN7EXAMPLE\"",
		Provider:  "openai",
		Model:     "gpt-5",
		Status:    StatusQueued,
	}
	if err := s.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if r.ID == "" {
		t.Fatal("Create did not assign an ID")
	}
	if err := validateID(r.ID); err != nil {
		t.Errorf("assigned ID %q is not a valid id: %v", r.ID, err)
	}
	if r.CreatedAt.IsZero() {
		t.Error("Create did not assign CreatedAt")
	}
	if r.UpdatedAt.IsZero() {
		t.Error("Create did not assign UpdatedAt")
	}

	// The file must exist on disk with mode 0600 (the review holds a plaintext
	// secret). Windows only exposes the read-only bit through FileMode, so the
	// permission-bit check is POSIX-only.
	fi, err := os.Stat(reviewPath(dir, r.ID))
	if err != nil {
		t.Fatalf("review file missing after Create: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("review file mode = %v, want 0600", fi.Mode().Perm())
	}

	got, ok := s.Get(r.ID)
	if !ok {
		t.Fatalf("Get(%q) reported missing after Create", r.ID)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("Get round-trip mismatch:\n got %+v\nwant %+v", got, r)
	}

	// The bytes on disk must be the same review (proves Create really wrote).
	data, err := os.ReadFile(reviewPath(dir, r.ID))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var onDisk Review
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("on-disk review is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(&onDisk, r) {
		t.Errorf("on-disk review mismatch:\n got %+v\nwant %+v", &onDisk, r)
	}
	if !strings.Contains(string(data), r.Secret) {
		t.Error("on-disk review does not contain the reviewed secret")
	}
}

func TestCreateHonoursSuppliedIDAndTimestamps(t *testing.T) {
	s, dir := newTestStore(t)
	created := time.Date(2024, 5, 4, 3, 2, 1, 0, time.UTC)

	r := &Review{ID: "custom-id_1", CreatedAt: created, Signature: "sig"}
	if err := s.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r.ID != "custom-id_1" {
		t.Errorf("ID = %q, want custom-id_1", r.ID)
	}
	if !r.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", r.CreatedAt, created)
	}
	if !r.UpdatedAt.Equal(created) {
		t.Errorf("UpdatedAt = %v, want it to default to CreatedAt %v", r.UpdatedAt, created)
	}
	if _, err := os.Stat(reviewPath(dir, "custom-id_1")); err != nil {
		t.Errorf("review file missing: %v", err)
	}

	// A second Create on the same id must not silently clobber the first.
	dup := &Review{ID: "custom-id_1", Signature: "other"}
	if err := s.Create(dup); err == nil {
		t.Fatal("duplicate Create succeeded, want error")
	} else if !errors.Is(err, os.ErrExist) {
		t.Errorf("duplicate Create error = %v, want errors.Is(err, os.ErrExist)", err)
	}
	got, _ := s.Get("custom-id_1")
	if got.Signature != "sig" {
		t.Errorf("duplicate Create overwrote the stored review: signature = %q", got.Signature)
	}

	if err := s.Create(nil); err == nil {
		t.Error("Create(nil) succeeded, want error")
	}
}

func TestListOrderingAndCount(t *testing.T) {
	s, _ := newTestStore(t)

	if got := s.Count(); got != 0 {
		t.Fatalf("Count() on empty store = %d, want 0", got)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("List() on empty store = %v, want empty", got)
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const n = 5
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		r := mustCreate(t, s, &Review{
			Signature: fmt.Sprintf("sig-%02d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Second),
			Status:    StatusQueued,
		})
		ids[i] = r.ID
	}

	if got := s.Count(); got != n {
		t.Fatalf("Count() = %d, want %d", got, n)
	}

	list := s.List()
	if len(list) != n {
		t.Fatalf("List() returned %d reviews, want %d", len(list), n)
	}
	for i := 0; i < n; i++ {
		want := ids[n-1-i] // newest first
		if list[i].ID != want {
			t.Errorf("List()[%d].ID = %q, want %q (newest first)", i, list[i].ID, want)
		}
		if want := fmt.Sprintf("sig-%02d", n-1-i); list[i].Signature != want {
			t.Errorf("List()[%d].Signature = %q, want %q", i, list[i].Signature, want)
		}
	}
}

func TestUpdatePersistsToDisk(t *testing.T) {
	s, dir := newTestStore(t)

	r := mustCreate(t, s, &Review{Signature: "sig", Status: StatusQueued})
	beforeUpdate := r.UpdatedAt
	time.Sleep(5 * time.Millisecond)

	got, ok := s.Get(r.ID)
	if !ok {
		t.Fatalf("Get(%q) missing", r.ID)
	}
	got.Status = StatusDone
	got.Assessment = "## Verdict\n\nThis is a hard-coded test credential."
	got.Messages = []Message{{Role: "assistant", Content: "reviewed", CreatedAt: time.Now().UTC()}}
	if err := s.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !got.UpdatedAt.After(beforeUpdate) {
		t.Errorf("Update did not bump UpdatedAt: %v (was %v)", got.UpdatedAt, beforeUpdate)
	}
	if !got.CreatedAt.Equal(r.CreatedAt) {
		t.Errorf("Update changed CreatedAt: %v, want %v", got.CreatedAt, r.CreatedAt)
	}

	// A brand-new Store over the same directory only knows what is on disk.
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(reopen): %v", err)
	}
	persisted, ok := reopened.Get(r.ID)
	if !ok {
		t.Fatalf("Get(%q) missing after reopen", r.ID)
	}
	if persisted.Status != StatusDone {
		t.Errorf("Status = %q after reopen, want %q", persisted.Status, StatusDone)
	}
	if persisted.Assessment != got.Assessment {
		t.Errorf("Assessment = %q after reopen, want %q", persisted.Assessment, got.Assessment)
	}
	if len(persisted.Messages) != 1 || persisted.Messages[0].Content != "reviewed" {
		t.Errorf("Messages = %+v after reopen, want one assistant turn", persisted.Messages)
	}
	if !persisted.UpdatedAt.Equal(got.UpdatedAt) {
		t.Errorf("UpdatedAt = %v after reopen, want %v", persisted.UpdatedAt, got.UpdatedAt)
	}
	if !persisted.CreatedAt.Equal(r.CreatedAt) {
		t.Errorf("CreatedAt = %v after reopen, want %v", persisted.CreatedAt, r.CreatedAt)
	}

	if err := s.Update(nil); err == nil {
		t.Error("Update(nil) succeeded, want error")
	}
}

func TestAppendMessage(t *testing.T) {
	s, dir := newTestStore(t)

	r := mustCreate(t, s, &Review{Signature: "sig", Status: StatusQueued})

	// A review may be chatted with while the model is still streaming.
	got, _ := s.Get(r.ID)
	got.Status = StatusRunning
	if err := s.Update(got); err != nil {
		t.Fatalf("Update to running: %v", err)
	}

	before := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)

	if err := s.AppendMessage(r.ID, Message{Role: "user", Content: "why is this a secret?"}); err != nil {
		t.Fatalf("AppendMessage(user): %v", err)
	}
	if err := s.AppendMessage(r.ID, Message{Role: "assistant", Content: "because the prefix is an AWS key id"}); err != nil {
		t.Fatalf("AppendMessage(assistant): %v", err)
	}

	got, ok := s.Get(r.ID)
	if !ok {
		t.Fatalf("Get(%q) missing", r.ID)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "user" || got.Messages[0].Content != "why is this a secret?" {
		t.Errorf("Messages[0] = %+v, want the user turn first", got.Messages[0])
	}
	if got.Messages[1].Role != "assistant" || got.Messages[1].Content != "because the prefix is an AWS key id" {
		t.Errorf("Messages[1] = %+v, want the assistant turn second", got.Messages[1])
	}
	for i, m := range got.Messages {
		if m.CreatedAt.IsZero() {
			t.Errorf("Messages[%d].CreatedAt was not populated", i)
		}
	}
	if !got.UpdatedAt.After(before) {
		t.Errorf("AppendMessage did not bump UpdatedAt: %v (before %v)", got.UpdatedAt, before)
	}
	if got.Status != StatusRunning {
		t.Errorf("AppendMessage changed Status to %q, want it left running", got.Status)
	}

	// Both turns must survive a reload from disk.
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(reopen): %v", err)
	}
	persisted, ok := reopened.Get(r.ID)
	if !ok {
		t.Fatalf("Get(%q) missing after reopen", r.ID)
	}
	if !reflect.DeepEqual(persisted.Messages, got.Messages) {
		t.Errorf("Messages after reopen = %+v, want %+v", persisted.Messages, got.Messages)
	}
	if !persisted.UpdatedAt.Equal(got.UpdatedAt) {
		t.Errorf("UpdatedAt after reopen = %v, want %v", persisted.UpdatedAt, got.UpdatedAt)
	}
}

func TestAppendMessagePreservesExplicitCreatedAt(t *testing.T) {
	s, _ := newTestStore(t)
	r := mustCreate(t, s, &Review{Signature: "sig"})

	stamp := time.Date(2020, 2, 2, 2, 2, 2, 0, time.UTC)
	if err := s.AppendMessage(r.ID, Message{Role: "user", Content: "hi", CreatedAt: stamp}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	got, _ := s.Get(r.ID)
	if len(got.Messages) != 1 || !got.Messages[0].CreatedAt.Equal(stamp) {
		t.Errorf("Messages = %+v, want the supplied CreatedAt %v preserved", got.Messages, stamp)
	}
}

func TestDeleteRemovesIndexEntryAndFile(t *testing.T) {
	s, dir := newTestStore(t)

	keep := mustCreate(t, s, &Review{Signature: "keep"})
	drop := mustCreate(t, s, &Review{Signature: "drop"})

	path := reviewPath(dir, drop.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("review file missing before Delete: %v", err)
	}

	if err := s.Delete(drop.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get(drop.ID); ok {
		t.Error("Get found the review after Delete")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("review file still present after Delete: stat err = %v", err)
	}
	if got := s.Count(); got != 1 {
		t.Errorf("Count() = %d, want 1", got)
	}
	for _, r := range s.List() {
		if r.ID == drop.ID {
			t.Error("List still returns the deleted review")
		}
	}

	// Delete is not idempotent: the second call is a missing-id error.
	if err := s.Delete(drop.ID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("second Delete error = %v, want errors.Is(err, os.ErrNotExist)", err)
	}

	// The surviving review is untouched, and Delete survives a reload.
	if _, ok := s.Get(keep.ID); !ok {
		t.Error("Delete removed the wrong review")
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(reopen): %v", err)
	}
	if got := reopened.Count(); got != 1 {
		t.Errorf("Count() after reopen = %d, want 1", got)
	}
	if _, ok := reopened.Get(drop.ID); ok {
		t.Error("deleted review came back after reopen")
	}
}

func TestMissingIDBehaviour(t *testing.T) {
	s, dir := newTestStore(t)
	const missing = "20240101T000000.000000000-deadbeef00"

	if got, ok := s.Get(missing); ok || got != nil {
		t.Errorf("Get(missing) = (%v, %v), want (nil, false)", got, ok)
	}
	if err := s.Update(&Review{ID: missing, Signature: "x"}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Update(missing) error = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
	if err := s.Delete(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Delete(missing) error = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
	if err := s.AppendMessage(missing, Message{Role: "user", Content: "hi"}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("AppendMessage(missing) error = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
	// The error is a fmt.Errorf wrap, so the Unwrap chain must reach
	// os.ErrNotExist exactly. (The legacy os.IsNotExist predicate does not see
	// through fmt.Errorf wrapping by design in the standard library, so callers
	// must use errors.Is; that is asserted here so the contract stays explicit.)
	if err := s.Delete(missing); errors.Unwrap(err) != os.ErrNotExist {
		t.Errorf("Delete(missing) error %v unwraps to %v, want os.ErrNotExist", err, errors.Unwrap(err))
	}

	// Nothing may have been created on disk by the failures.
	if names := jsonFiles(t, dir); len(names) != 0 {
		t.Errorf("failed missing-id calls left files behind: %v", names)
	}
}

func TestInvalidIDsAreRejected(t *testing.T) {
	// The empty id is invalid everywhere except Create, where it means
	// "assign one"; it is included here for the lookup methods.
	bad := []string{
		"",
		"..",
		".",
		"../evil",
		"../../evil",
		"a/b",
		`a\b`,
		`..\evil`,
		`C:\Windows\evil`,
		"C:/Windows/evil",
		"with space",
		"semi;colon",
		"qu:ote",
		"star*",
		"pipe|",
		"new\nline",
		"tab\tchar",
		"nul",
		"CON",
		"com1",
		".hidden",
		strings.Repeat("x", maxIDLen+1),
		"\x00",
	}

	for i, id := range bad {
		t.Run(fmt.Sprintf("case_%02d", i), func(t *testing.T) {
			s, dir := newTestStore(t)

			if id == "" {
				// Create with an empty id assigns one instead of failing.
				r := &Review{Signature: "x"}
				if err := s.Create(r); err != nil || r.ID == "" {
					t.Fatalf("Create with empty ID: err = %v, id = %q; want an assigned id", err, r.ID)
				}
				if err := s.Delete(r.ID); err != nil {
					t.Fatalf("Delete(%q): %v", r.ID, err)
				}
			} else if err := s.Create(&Review{ID: id, Signature: "x"}); err == nil {
				t.Errorf("Create(ID=%q) succeeded, want error", id)
			} else if !errors.Is(err, os.ErrInvalid) {
				t.Errorf("Create(ID=%q) error = %v, want errors.Is(err, os.ErrInvalid)", id, err)
			}

			// Lookups must report the documented missing-id result.
			if got, ok := s.Get(id); ok || got != nil {
				t.Errorf("Get(%q) = (%v, %v), want (nil, false)", id, got, ok)
			}
			if err := s.Update(&Review{ID: id}); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("Update(%q) error = %v, want errors.Is(err, os.ErrNotExist)", id, err)
			}
			if err := s.Delete(id); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("Delete(%q) error = %v, want errors.Is(err, os.ErrNotExist)", id, err)
			}
			if err := s.AppendMessage(id, Message{Role: "user"}); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("AppendMessage(%q) error = %v, want errors.Is(err, os.ErrNotExist)", id, err)
			}
			if got := s.Count(); got != 0 {
				t.Errorf("Count() = %d after rejected ids, want 0", got)
			}

			// And nothing may have been written anywhere near the store.
			if names := jsonFiles(t, dir); len(names) != 0 {
				t.Errorf("files created inside the store directory: %v", names)
			}
		})
	}
}

func TestPathTraversalCannotEscapeStoreDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "store")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	escapes := []string{
		"../evil",
		"../../evil",
		`..\evil`,
		"..",
		".",
		"sub/evil",
	}
	for _, id := range escapes {
		if err := s.Create(&Review{ID: id, Signature: "x"}); err == nil {
			t.Errorf("Create(ID=%q) succeeded, want error", id)
		}
		if err := s.AppendMessage(id, Message{Role: "user"}); err == nil {
			t.Errorf("AppendMessage(%q) succeeded, want error", id)
		}
		if err := s.Delete(id); err == nil {
			t.Errorf("Delete(%q) succeeded, want error", id)
		}
	}

	// Nothing outside the store directory, and nothing inside it either.
	for _, candidate := range []string{
		filepath.Join(parent, "evil.json"),
		filepath.Join(parent, "..", "evil.json"),
		filepath.Join(parent, "sub", "evil.json"),
	} {
		if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("traversal attempt created %q (stat err = %v)", candidate, err)
		}
	}
	if names := jsonFiles(t, dir); len(names) != 0 {
		t.Errorf("traversal attempts left files in the store: %v", names)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", parent, err)
	}
	if len(entries) != 1 || entries[0].Name() != "store" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("store parent contains %v, want only the store directory", names)
	}
}

func TestGetAndListReturnDeepCopies(t *testing.T) {
	s, _ := newTestStore(t)

	original := &Review{
		Signature:  "sig",
		Assessment: "original",
		Status:     StatusDone,
		Messages:   []Message{{Role: "user", Content: "first", CreatedAt: time.Now().UTC()}},
	}
	mustCreate(t, s, original)

	// Mutating the caller's own value after Create must not reach the store.
	original.Assessment = "mutated-after-create"
	original.Messages[0].Content = "mutated-after-create"
	original.Messages = append(original.Messages, Message{Role: "user", Content: "extra"})

	// Mutating a value handed out by Get must not reach the store.
	first, ok := s.Get(original.ID)
	if !ok {
		t.Fatalf("Get(%q) missing", original.ID)
	}
	first.Assessment = "mutated-from-get"
	first.Status = StatusFailed
	first.Messages[0].Content = "mutated-from-get"
	first.Messages = append(first.Messages, Message{Role: "assistant", Content: "extra-from-get"})

	// Mutating a value handed out by List must not reach the store.
	list := s.List()
	if len(list) != 1 {
		t.Fatalf("List() returned %d reviews, want 1", len(list))
	}
	list[0].Assessment = "mutated-from-list"
	list[0].Messages[0].Content = "mutated-from-list"
	list[0].Messages = append(list[0].Messages, Message{Role: "assistant", Content: "extra-from-list"})

	second, ok := s.Get(original.ID)
	if !ok {
		t.Fatalf("Get(%q) missing on second read", original.ID)
	}
	if second.Assessment != "original" {
		t.Errorf("Assessment = %q, want %q (stored state was mutated)", second.Assessment, "original")
	}
	if second.Status != StatusDone {
		t.Errorf("Status = %q, want %q", second.Status, StatusDone)
	}
	if len(second.Messages) != 1 {
		t.Fatalf("Messages = %d, want 1: %+v", len(second.Messages), second.Messages)
	}
	if second.Messages[0].Content != "first" {
		t.Errorf("Messages[0].Content = %q, want %q", second.Messages[0].Content, "first")
	}

	// Copies handed out twice must not share backing storage either.
	a, _ := s.Get(original.ID)
	b, _ := s.Get(original.ID)
	a.Messages[0].Content = "tampered"
	if b.Messages[0].Content != "first" {
		t.Errorf("two Get copies share backing storage: b.Messages[0].Content = %q", b.Messages[0].Content)
	}

	// The same holds for a store reopened over the same directory.
	other, _ := s.Get(original.ID)
	other.Messages[0].Role = "assistant"
	stored, _ := s.Get(original.ID)
	if stored.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want %q", stored.Messages[0].Role, "user")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s, dir := newTestStore(t)

	const workers = 24
	const messagesPerWorker = 5
	const readers = 8
	const readsPerReader = 40

	// One review is hammered by every worker and reader at once, which is
	// where lost updates would show up.
	shared := mustCreate(t, s, &Review{Signature: "shared", Status: StatusRunning})

	var wg sync.WaitGroup
	errCh := make(chan error, workers*8+readers)

	ids := make([]string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			r := &Review{Signature: fmt.Sprintf("sig-%02d", i), Status: StatusQueued}
			if err := s.Create(r); err != nil {
				errCh <- fmt.Errorf("Create(%d): %w", i, err)
				return
			}
			ids[i] = r.ID

			for j := 0; j < messagesPerWorker; j++ {
				if err := s.AppendMessage(r.ID, Message{Role: "user", Content: fmt.Sprintf("m-%d-%d", i, j)}); err != nil {
					errCh <- fmt.Errorf("AppendMessage(%d,%d): %w", i, j, err)
					return
				}
			}

			got, ok := s.Get(r.ID)
			if !ok {
				errCh <- fmt.Errorf("Get(%q) missing after Create", r.ID)
				return
			}
			got.Status = StatusDone
			got.Assessment = fmt.Sprintf("assessment-%02d", i)
			if err := s.Update(got); err != nil {
				errCh <- fmt.Errorf("Update(%d): %w", i, err)
				return
			}
			if err := s.AppendMessage(r.ID, Message{Role: "assistant", Content: "done"}); err != nil {
				errCh <- fmt.Errorf("AppendMessage(assistant,%d): %w", i, err)
				return
			}

			// Same-id contention on the shared review.
			if err := s.AppendMessage(shared.ID, Message{Role: "user", Content: fmt.Sprintf("shared-%d", i)}); err != nil {
				errCh <- fmt.Errorf("AppendMessage(shared,%d): %w", i, err)
			}
		}(i)
	}

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < readsPerReader; j++ {
				got, ok := s.Get(shared.ID)
				if !ok || got == nil {
					errCh <- fmt.Errorf("reader %d: Get(shared) reported missing", i)
					return
				}
				if list := s.List(); len(list) == 0 {
					errCh <- fmt.Errorf("reader %d: List() is empty while reviews exist", i)
					return
				}
				if n := s.Count(); n < 1 {
					errCh <- fmt.Errorf("reader %d: Count() = %d", i, n)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent access: %v", err)
	}

	if got, want := s.Count(), workers+1; got != want {
		t.Fatalf("Count() = %d, want %d", got, want)
	}
	for i := 0; i < workers; i++ {
		got, ok := s.Get(ids[i])
		if !ok {
			t.Errorf("review %d (%q) missing after the concurrent run", i, ids[i])
			continue
		}
		if got.Signature != fmt.Sprintf("sig-%02d", i) {
			t.Errorf("review %d signature = %q", i, got.Signature)
		}
		if got.Status != StatusDone {
			t.Errorf("review %d status = %q, want %q", i, got.Status, StatusDone)
		}
		if want := messagesPerWorker + 1; len(got.Messages) != want {
			t.Errorf("review %d has %d messages, want %d (lost update)", i, len(got.Messages), want)
		}
	}

	gotShared, ok := s.Get(shared.ID)
	if !ok {
		t.Fatalf("shared review missing after the concurrent run")
	}
	if len(gotShared.Messages) != workers {
		t.Errorf("shared review has %d messages, want %d (lost updates on the same id)", len(gotShared.Messages), workers)
	}

	// Everything must have reached the disk, and nothing may be left over.
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(reopen): %v", err)
	}
	if got, want := reopened.Count(), workers+1; got != want {
		t.Errorf("Count() after reopen = %d, want %d", got, want)
	}
	if got, want := len(reopened.List()), workers+1; got != want {
		t.Errorf("len(List()) after reopen = %d, want %d", got, want)
	}
	for _, name := range jsonFiles(t, dir) {
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, jsonExt) {
			t.Errorf("leftover temp file after concurrent run: %q", name)
		}
	}
}

func TestCorruptionTolerance(t *testing.T) {
	s, dir := newTestStore(t)

	first := mustCreate(t, s, &Review{Signature: "first", Status: StatusQueued})
	second := mustCreate(t, s, &Review{Signature: "second", Status: StatusQueued})

	// Junk that is not JSON at all, a truncated write, valid JSON of the wrong
	// shape, a file whose name is not a legal id, and an empty file.
	fixtures := map[string]string{
		"bogus.json":      "{this is not json",
		"truncated.json":  `{"id":"truncated","signature":"half`,
		"wrongshape.json": `["not","an","object"]`,
		"bad name.json":   `{"id":"bad name","signature":"x"}`,
		"emptyfile.json":  "",
	}
	for name, body := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture %q: %v", name, err)
		}
	}

	// The live store must be unaffected.
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("List() = %d reviews, want 2 (corrupt files must be ignored): %+v", len(list), list)
	}
	if got := s.Count(); got != 2 {
		t.Errorf("Count() = %d, want 2", got)
	}
	for _, r := range list {
		if r.ID != first.ID && r.ID != second.ID {
			t.Errorf("List() returned unexpected review %q", r.ID)
		}
	}
	if _, ok := s.Get("bogus"); ok {
		t.Error("Get(\"bogus\") found a review in a corrupt file")
	}

	// A fresh store over the same directory must load the readable reviews and
	// skip the corrupt ones without failing.
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore over a corrupt directory: %v", err)
	}
	if got := reopened.Count(); got != 2 {
		t.Errorf("Count() after reopen = %d, want 2", got)
	}
	got, ok := reopened.Get(first.ID)
	if !ok {
		t.Fatalf("Get(%q) missing after reopen", first.ID)
	}
	if got.Signature != "first" {
		t.Errorf("Signature = %q, want %q", got.Signature, "first")
	}

	// And the corrupt files stay corrupt: the store never rewrote them.
	data, err := os.ReadFile(filepath.Join(dir, "bogus.json"))
	if err != nil {
		t.Fatalf("ReadFile(bogus.json): %v", err)
	}
	if string(data) != fixtures["bogus.json"] {
		t.Errorf("bogus.json was modified: %q", data)
	}
}

func TestPartsOfAnInterruptedWriteAreIgnored(t *testing.T) {
	s, dir := newTestStore(t)
	valid := mustCreate(t, s, &Review{Signature: "valid"})

	// Simulate a crash mid-write: a torn temp file plus a hidden one.
	for _, name := range []string{tmpPrefix + "123456", tmpPrefix + "abcdef.json", ".tmp-partial.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"id":"ghost","signature":"ghost"`), 0o600); err != nil {
			t.Fatalf("write temp fixture %q: %v", name, err)
		}
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := reopened.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1 (temp files are not reviews)", got)
	}
	if _, ok := reopened.Get("ghost"); ok {
		t.Error("Get(\"ghost\") loaded a leftover temp file as a review")
	}
	if _, ok := reopened.Get(valid.ID); !ok {
		t.Errorf("Get(%q) missing after reopen", valid.ID)
	}
}

func TestNoTempFilesSurviveMutations(t *testing.T) {
	s, dir := newTestStore(t)

	r := mustCreate(t, s, &Review{Signature: "sig"})
	if err := s.AppendMessage(r.ID, Message{Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	got, _ := s.Get(r.ID)
	got.Status = StatusDone
	if err := s.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}

	for _, name := range jsonFiles(t, dir) {
		if strings.HasPrefix(name, tmpPrefix) || strings.HasPrefix(name, ".") {
			t.Errorf("mutation left a temp file behind: %q", name)
		}
		if !strings.HasSuffix(name, jsonExt) {
			t.Errorf("unexpected file in the store directory: %q", name)
		}
	}
}

func TestIDsAreUniqueAndTimeSortable(t *testing.T) {
	s, _ := newTestStore(t)

	const n = 100
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		r := mustCreate(t, s, &Review{Signature: fmt.Sprintf("sig-%03d", i)})
		ids = append(ids, r.ID)
	}

	seen := make(map[string]bool, n)
	var prev time.Time
	for i, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id generated: %q", id)
		}
		seen[id] = true

		if len(id) <= len(idTimeLayout) {
			t.Fatalf("id %q is too short to carry a time prefix", id)
		}
		prefix := id[:len(idTimeLayout)]
		ts, err := time.Parse(idTimeLayout, prefix)
		if err != nil {
			t.Fatalf("id %q does not start with a parseable %q prefix: %v", id, idTimeLayout, err)
		}
		// Fixed-width, zero-padded prefixes: non-decreasing time implies
		// lexicographic order matches creation order for the file names.
		if !prev.IsZero() && ts.Before(prev) {
			t.Errorf("id %d (%q) went backwards in time: %v < %v", i, id, ts, prev)
		}
		if i > 0 && prefix < ids[i-1][:len(idTimeLayout)] {
			t.Errorf("time prefix is not lexicographically non-decreasing: %q then %q", ids[i-1], id)
		}
		prev = ts
	}
}

func TestNewStoreOverExistingDirectoryAndErrors(t *testing.T) {
	if _, err := NewStore(""); err == nil {
		t.Error("NewStore(\"\") succeeded, want error")
	}
	if _, err := NewStore("   "); err == nil {
		t.Error("NewStore(\"   \") succeeded, want error")
	}

	// Nested directories are created on demand.
	base := t.TempDir()
	deep := filepath.Join(base, "a", "b", "c")
	s, err := NewStore(deep)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", deep, err)
	}
	r := mustCreate(t, s, &Review{Signature: "sig"})
	if _, err := os.Stat(reviewPath(deep, r.ID)); err != nil {
		t.Errorf("review file missing in nested directory: %v", err)
	}

	// Reopening the same directory twice must not duplicate anything.
	again, err := NewStore(deep)
	if err != nil {
		t.Fatalf("NewStore(again): %v", err)
	}
	if got := again.Count(); got != 1 {
		t.Errorf("Count() = %d, want 1", got)
	}
}

func TestStatusConstants(t *testing.T) {
	want := map[Status]string{
		StatusQueued:  "queued",
		StatusRunning: "running",
		StatusDone:    "done",
		StatusFailed:  "failed",
	}
	for st, s := range want {
		if string(st) != s {
			t.Errorf("Status constant = %q, want %q", st, s)
		}
	}
}

func TestReviewJSONFieldNames(t *testing.T) {
	// The on-disk shape is a compatibility contract for older files.
	r := Review{ID: "i", Messages: []Message{{Role: "user"}}}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, field := range []string{
		`"id"`, `"created_at"`, `"updated_at"`, `"signature"`, `"secret"`, `"file"`,
		`"url"`, `"repo"`, `"context"`, `"provider"`, `"model"`, `"status"`,
		`"assessment"`, `"error"`, `"messages"`, `"role"`, `"content"`,
	} {
		if !strings.Contains(string(data), field) {
			t.Errorf("marshalled review is missing JSON key %s: %s", field, data)
		}
	}
}
