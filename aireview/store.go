package aireview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// JobStore persists jobs under root/jobs/<id>/ with atomic whole-file
// replaces for job.json and an append-only logs.txt.
type JobStore struct {
	root string
	jobs string
	mu   sync.Mutex
}

// NewJobStore creates the jobs directory under root.
func NewJobStore(root string) (*JobStore, error) {
	dir := filepath.Join(root, "jobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &JobStore{root: root, jobs: dir}, nil
}

// jobDir returns the directory for one job. Exported for tests.
func (s *JobStore) jobDir(id string) string { return filepath.Join(s.jobs, id) }

// Create writes a new job; fails if the job directory already exists.
func (s *JobStore) Create(j *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.jobDir(j.ID)
	if _, err := os.Stat(filepath.Join(dir, "job.json")); err == nil {
		return fmt.Errorf("job %s already exists", j.ID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	now := time.Now().UTC()
	j.CreatedAt = now
	j.UpdatedAt = now
	return atomicWriteJSON(filepath.Join(dir, "job.json"), j)
}

// Get returns the job with the given id, or os.ErrNotExist.
func (s *JobStore) Get(id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJob(filepath.Join(s.jobDir(id), "job.json"))
}

// Update atomically replaces the job file and bumps UpdatedAt.
func (s *JobStore) Update(j *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.UpdatedAt = time.Now().UTC()
	return atomicWriteJSON(filepath.Join(s.jobDir(j.ID), "job.json"), j)
}

// List returns all jobs, newest first, skipping unreadable entries.
func (s *JobStore) List() ([]*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.jobs)
	if err != nil {
		return nil, err
	}
	var jobs []*Job
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		j, err := readJob(filepath.Join(s.jobs, e.Name(), "job.json"))
		if err != nil {
			continue
		}
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].CreatedAt.After(jobs[k].CreatedAt) })
	return jobs, nil
}

// AppendLog appends one timestamped line to the job's logs.txt.
func (s *JobStore) AppendLog(id, line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.jobDir(id), "logs.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
	return err
}

// ReadLogTail returns the last n lines of logs.txt (all lines when fewer).
func (s *JobStore) ReadLogTail(id string, n int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.jobDir(id), "logs.txt"))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n && n > 0 {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

func atomicWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJob(path string) (*Job, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	return &j, nil
}
