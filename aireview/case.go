package aireview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Verification is a cached live-check result for one credential.
type Verification struct {
	CheckedAt    time.Time `json:"checked_at"`
	Valid        bool      `json:"valid"`
	Scope        string    `json:"scope"`
	EvidenceRefs []string  `json:"evidence_refs"`
}

// Case unifies jobs that share a secret fingerprint.
type Case struct {
	Fingerprint  string        `json:"fingerprint"`
	FirstSeen    time.Time     `json:"first_seen"`
	Verdict      string        `json:"verdict"`
	Verification *Verification `json:"verification,omitempty"`
	Jobs         []string      `json:"jobs"`
}

// Fresh reports whether the case's cached verification is newer than ttl.
func (c *Case) Fresh(ttl time.Duration, now time.Time) bool {
	if c.Verification == nil {
		return false
	}
	return now.Sub(c.Verification.CheckedAt) <= ttl
}

// CaseStore persists cases under root/cases/<fingerprint>/case.json.
type CaseStore struct {
	dir string
	mu  sync.Mutex
}

// NewCaseStore creates the cases directory under root.
func NewCaseStore(root string) (*CaseStore, error) {
	dir := filepath.Join(root, "cases")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &CaseStore{dir: dir}, nil
}

func (s *CaseStore) casePath(fp string) string { return filepath.Join(s.dir, fp, "case.json") }

// Get returns the case for a fingerprint, or os.ErrNotExist.
func (s *CaseStore) Get(fp string) (*Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(fp)
}

// Create persists a new case.
func (s *CaseStore) Create(c *Case) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.dir, c.Fingerprint), 0o755); err != nil {
		return err
	}
	return atomicWriteJSON(s.casePath(c.Fingerprint), c)
}

// Update atomically replaces a case.
func (s *CaseStore) Update(c *Case) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWriteJSON(s.casePath(c.Fingerprint), c)
}

// AddJob links a job id to the case, idempotently.
func (s *CaseStore) AddJob(fp, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.getLocked(fp)
	if err != nil {
		return err
	}
	for _, id := range c.Jobs {
		if id == jobID {
			return nil
		}
	}
	c.Jobs = append(c.Jobs, jobID)
	return atomicWriteJSON(s.casePath(fp), c)
}

// List returns all cases, newest FirstSeen first.
func (s *CaseStore) List() ([]*Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var cases []*Case
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := s.getLocked(e.Name())
		if err != nil {
			continue
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, k int) bool { return cases[i].FirstSeen.After(cases[k].FirstSeen) })
	return cases, nil
}

func (s *CaseStore) getLocked(fp string) (*Case, error) {
	b, err := os.ReadFile(s.casePath(fp))
	if err != nil {
		return nil, err
	}
	var c Case
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
