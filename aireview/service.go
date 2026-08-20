package aireview

import (
	"fmt"
	"time"
)

// dudVerdicts are terminal verdicts from the Stage-0 gate (and cached cases)
// that short-circuit a job before any clone or LLM spend.
var dudVerdicts = map[string]bool{
	VerdictPlaceholder: true,
	VerdictMalformed:   true,
	VerdictRevoked:     true,
	VerdictTestFixture: true,
}

// Service is the app-side facade over the job store, case store,
// notifications, clone pool, and Stage-0 gate.
type Service struct {
	Jobs    *JobStore
	Cases   *CaseStore
	Notify  *NotifyStore
	Pool    *ClonePool
	Gate    *Gate
	Webhook string
	BaseURL string
	root    string
	onEvent func(job *Job)
}

// NewService wires the stores and pool together. cloneFn is the real clone
// implementation (see web_ai.go); tests inject a fake.
func NewService(root, webhook, baseURL string, cloneFn CloneFn, gate *Gate) (*Service, error) {
	js, err := NewJobStore(root)
	if err != nil {
		return nil, err
	}
	cs, err := NewCaseStore(root)
	if err != nil {
		return nil, err
	}
	ns, err := NewNotifyStore(root)
	if err != nil {
		return nil, err
	}
	if cloneFn == nil {
		cloneFn = func(dst, url string) error { return fmt.Errorf("no clone implementation configured") }
	}
	return &Service{
		Jobs:    js,
		Cases:   cs,
		Notify:  ns,
		Pool:    NewClonePool(root, cloneFn),
		Gate:    gate,
		Webhook: webhook,
		BaseURL: baseURL,
		root:    root,
	}, nil
}

// SetOnEvent registers a callback fired whenever a job changes. The web
// layer uses it to push SSE events.
func (s *Service) SetOnEvent(fn func(job *Job)) { s.onEvent = fn }

func (s *Service) emit(j *Job) {
	if s.onEvent != nil {
		s.onEvent(j)
	}
}

func (s *Service) notify(j *Job, kind NotifyKind, level, message string) {
	_ = s.Notify.Append(Notification{
		ID:        fmt.Sprintf("%d-%s", time.Now().UnixNano(), j.ID),
		CreatedAt: time.Now().UTC(),
		Level:     level,
		Kind:      kind,
		JobID:     j.ID,
		Message:   message,
	})
	s.emit(j)
}

// Flag creates a new queued job (or a dud job when the case cache already
// holds a definitive verdict for this secret).
func (s *Service) Flag(repoURL, file, signature, secret, matchContext string, stars int, authorized bool) (*Job, error) {
	if repoURL == "" || file == "" || signature == "" || secret == "" {
		return nil, fmt.Errorf("repo_url, file, signature, and secret are required")
	}
	fp := Fingerprint(secret)
	id := fmt.Sprintf("%d-%s", time.Now().UnixNano(), slugSafe(repoURL))
	j := &Job{
		ID:                id,
		RepoURL:           repoURL,
		File:              file,
		Signature:         signature,
		Secret:            secret,
		SecretFingerprint: fp,
		MatchContext:      matchContext,
		Stars:             stars,
		Authorized:        authorized,
		State:             StateQueued,
		CaseID:            fp,
	}
	if c, err := s.Cases.Get(fp); err == nil && dudVerdicts[c.Verdict] {
		j.State = StateDud
		j.Verdict = c.Verdict
		j.ReviewNeeded = true
		j.StageName = "Pre-validation (cached case verdict)"
	} else if err != nil {
		_ = s.Cases.Create(&Case{Fingerprint: fp, FirstSeen: time.Now().UTC()})
	}
	if err := s.Jobs.Create(j); err != nil {
		return nil, err
	}
	_ = s.Cases.AddJob(fp, id)
	_ = s.Jobs.AppendLog(id, "Flagged for AI review (authorized="+fmt.Sprintf("%v", authorized)+")")
	if j.State == StateDud {
		s.notify(j, NotifyDud, "warn", "Flagged secret already known "+j.Verdict)
	}
	s.emit(j)
	return j, nil
}

// Start runs the Stage-0 gate and, when the secret passes, kicks off the
// background clone. Dud verdicts short-circuit with review_needed.
func (s *Service) Start(id string) error {
	j, err := s.Jobs.Get(id)
	if err != nil {
		return err
	}
	if j.State == StateRunning {
		return nil // already started; don't re-run the gate or spawn another clone
	}
	if err := Transition(j.State, StateRunning); err != nil {
		return err
	}
	j.State = StateRunning
	j.Stage = 0
	j.StageName = "Pre-validation"
	if err := s.Jobs.Update(j); err != nil {
		return err
	}
	_ = s.Jobs.AppendLog(id, "Starting: Stage 0 pre-validation")
	verdict, reason := s.Gate.Evaluate(j)
	if dudVerdicts[verdict] {
		j.State = StateDud
		j.Verdict = verdict
		j.ReviewNeeded = true
		j.StageName = "Pre-validation (dud: " + verdict + ")"
		_ = s.Jobs.Update(j)
		_ = s.Jobs.AppendLog(id, "DUD: "+reason+" (review needed)")
		// Read-modify-write: preserve the case's Jobs links, FirstSeen, and
		// any cached Verification — only the verdict changes.
		if c, err := s.Cases.Get(j.SecretFingerprint); err == nil {
			c.Verdict = verdict
			_ = s.Cases.Update(c)
		}
		s.notify(j, NotifyDud, "warn", "Stage 0: "+reason)
		return nil
	}
	j.Verdict = VerdictLikelyReal
	_ = s.Jobs.Update(j)
	_ = s.Jobs.AppendLog(id, "Stage 0 passed: "+reason+"; cloning repo")
	go s.cloneInBackground(id)
	return nil
}

func (s *Service) cloneInBackground(id string) {
	j, err := s.Jobs.Get(id)
	if err != nil {
		return
	}
	path, reused, err := s.Pool.Ensure(j.RepoURL)
	if err != nil {
		// Re-read so the failure does not clobber state changes
		// (pause/resume/stop) made while the clone was in flight.
		if cur, err := s.Jobs.Get(id); err == nil {
			j = cur
		}
		j.State = StateFailed
		j.Error = "clone failed: " + err.Error()
		_ = s.Jobs.Update(j)
		_ = s.Jobs.AppendLog(id, "Clone FAILED: "+err.Error())
		s.notify(j, NotifyFailed, "error", "Clone failed")
		return
	}
	// Re-read the job so the clone result does not clobber state changes
	// (pause/resume/stop) made while the clone was in flight.
	if cur, err := s.Jobs.Get(id); err == nil {
		j = cur
	}
	j.ClonePath = path
	_ = s.Jobs.Update(j)
	if reused {
		_ = s.Jobs.AppendLog(id, "Clone reused from pool: "+path)
	} else {
		_ = s.Jobs.AppendLog(id, "Clone complete: "+path)
	}
	s.emit(j)
}

// Pause transitions running -> paused.
func (s *Service) Pause(id string) error {
	j, err := s.Jobs.Get(id)
	if err != nil {
		return err
	}
	if err := Transition(j.State, StatePaused); err != nil {
		return err
	}
	j.State = StatePaused
	_ = s.Jobs.Update(j)
	_ = s.Jobs.AppendLog(id, "Paused (workflow will halt at next stage checkpoint)")
	s.emit(j)
	return nil
}

// Resume transitions paused -> running.
func (s *Service) Resume(id string) error {
	j, err := s.Jobs.Get(id)
	if err != nil {
		return err
	}
	if err := Transition(j.State, StateRunning); err != nil {
		return err
	}
	j.State = StateRunning
	_ = s.Jobs.Update(j)
	_ = s.Jobs.AppendLog(id, "Resumed")
	s.emit(j)
	return nil
}

// Stop transitions any active state -> cancelled.
func (s *Service) Stop(id string) error {
	j, err := s.Jobs.Get(id)
	if err != nil {
		return err
	}
	if j.State == StateCancelled {
		return fmt.Errorf("job %s is already cancelled", id)
	}
	if err := Transition(j.State, StateCancelled); err != nil {
		return err
	}
	j.State = StateCancelled
	_ = s.Jobs.Update(j)
	_ = s.Jobs.AppendLog(id, "Stopped by operator")
	s.emit(j)
	return nil
}

// Archive zips the job artifacts and returns the zip path.
func (s *Service) Archive(id string) (string, error) {
	zipPath, err := ArchiveJob(s.root, id)
	if err != nil {
		return "", err
	}
	if j, err := s.Jobs.Get(id); err == nil {
		s.notify(j, NotifyArchived, "info", "Job archived to "+zipPath)
	}
	return zipPath, nil
}

// slugSafe derives a short filesystem-safe slug from a repo URL.
func slugSafe(repoURL string) string {
	out := make([]byte, 0, len(repoURL))
	for i := 0; i < len(repoURL); i++ {
		c := repoURL[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	s := string(out)
	for len(s) > 0 && s[len(s)-1] == '-' {
		s = s[:len(s)-1]
	}
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "repo"
	}
	return s
}
