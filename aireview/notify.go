package aireview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// NotifyKind identifies the event that produced a notification.
type NotifyKind string

const (
	NotifyCompleted NotifyKind = "job_completed"
	NotifyFailed    NotifyKind = "job_failed"
	NotifyDud       NotifyKind = "job_dud"
	NotifyArchived  NotifyKind = "job_archived"
)

// Notification is one in-app alert entry.
type Notification struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	Level     string     `json:"level"`
	Kind      NotifyKind `json:"kind"`
	JobID     string     `json:"job_id"`
	Message   string     `json:"message"`
	Read      bool       `json:"read"`
}

// NotifyStore is a file-backed notification list (root/notifications.json).
type NotifyStore struct {
	path string
	mu   sync.Mutex
}

// NewNotifyStore creates the store file location under root.
func NewNotifyStore(root string) (*NotifyStore, error) {
	path := filepath.Join(root, "notifications.json")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &NotifyStore{path: path}, nil
}

func (s *NotifyStore) loadLocked() ([]Notification, error) {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Notification
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (s *NotifyStore) saveLocked(list []Notification) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Append adds a notification and persists.
func (s *NotifyStore) Append(n Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadLocked()
	if err != nil {
		return err
	}
	list = append(list, n)
	return s.saveLocked(list)
}

// List returns notifications, newest first.
func (s *NotifyStore) List() ([]Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, k int) bool { return list[i].CreatedAt.After(list[k].CreatedAt) })
	return list, nil
}

// MarkAllRead marks every notification read and persists.
func (s *NotifyStore) MarkAllRead() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i := range list {
		list[i].Read = true
	}
	return s.saveLocked(list)
}

// UnreadCount returns the number of unread notifications.
func (s *NotifyStore) UnreadCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadLocked()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range list {
		if !x.Read {
			n++
		}
	}
	return n, nil
}
