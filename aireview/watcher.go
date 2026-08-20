package aireview

import (
	"time"
)

// watchStop stops the watcher goroutine (used by tests).
func (s *Service) watchStop() {
	s.watchDone <- struct{}{}
}

// StartWatcher polls the job store and emits notifications + webhooks when a
// job reaches a terminal state that the DSH workflow wrote directly
// (done/failed), which the app itself never transitions to.
func (s *Service) StartWatcher(interval time.Duration) {
	s.watchDone = make(chan struct{})
	go func() {
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				s.watchOnce()
			case <-s.watchDone:
				return
			}
		}
	}()
}

func (s *Service) watchOnce() {
	jobs, err := s.Jobs.List()
	if err != nil {
		return
	}
	for _, j := range jobs {
		if s.emitted == nil {
			s.emitted = map[string]State{}
		}
		prev, seen := s.emitted[j.ID]
		if seen && prev == j.State {
			continue
		}
		s.emitted[j.ID] = j.State
		switch j.State {
		case StateDone:
			_ = s.Jobs.AppendLog(j.ID, "Review complete")
			s.notify(j, NotifyCompleted, "success", "AI review completed")
			s.postTerminalWebhook(j, "AI Review Complete", 0x57b35c)
		case StateFailed:
			s.notify(j, NotifyFailed, "error", "AI review failed")
			s.postTerminalWebhook(j, "AI Review Failed", 0xd9534f)
		}
	}
}

func (s *Service) postTerminalWebhook(j *Job, title string, color int) {
	if s.Webhook == "" {
		return
	}
	desc := j.RepoURL + "\n" + j.File + "\n" + j.Signature
	if j.Verdict != "" {
		desc += "\nVerdict: " + j.Verdict
	}
	if s.BaseURL != "" {
		desc += "\n" + s.BaseURL + "/#ai/" + j.ID
	}
	s.SendDiscordEmbed(title, desc, color)
}
