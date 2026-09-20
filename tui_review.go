// tui_review.go - the bridge between the interactive terminal UI and the AI
// review engine. It is deliberately separate from tui.go so the renderer stays
// free of review-store dependencies: runTUIMode installs tuiStartReview as the
// "r" key's hook, and every entry point here is safe to call at any time.
//
// A review is created and started exactly the way POST /api/review does it -
// same store, same provider client, same prompt builder - so the terminal and
// the dashboard produce identical reviews for the same finding.

package main

import (
	"errors"
	"strings"

	"github.com/trebor048/shhgot/reviewstore"
)

// tuiStartReview creates a review for one finding, starts the provider call in
// the background, and streams the result into the detail pane. It returns as
// soon as the review is queued; the model runs on its own goroutine.
func tuiStartReview(m *TUIMatch) error {
	if m == nil {
		return errors.New("no finding selected")
	}
	if reviewStore == nil {
		// AI review is optional and is initialized by runTUIMode; without it
		// there is nothing to start.
		return errors.New("not available (no AI provider configured)")
	}
	client, settings, err := reviewClient()
	if err != nil {
		return err
	}

	// The captured file body is what lets the model judge the surrounding code,
	// exactly as the dashboard's review request does. It is optional: a finding
	// whose file was not kept still gets a metadata-only review.
	context := ""
	if d, ok := tuiLookupMatchFile(m.ID); ok {
		context = d.Content
	}

	rev := &reviewstore.Review{
		Signature: strings.TrimSpace(m.Signature),
		Secret:    strings.TrimSpace(m.Secret),
		File:      strings.TrimSpace(m.File),
		URL:       strings.TrimSpace(m.URL),
		Repo:      strings.TrimSpace(tuiMatchRepoLabel(m)),
		Context:   context,
		Provider:  settings.Provider,
		Model:     effectiveModel(settings),
		Status:    reviewstore.StatusQueued,
	}
	if err := reviewStore.Create(rev); err != nil {
		return err
	}

	id := rev.ID
	// Announce the review before the first delta arrives so the detail pane
	// shows the spinner immediately.
	SetTUIReview(m.ID, "", true)
	startReview(rev, client)
	go tuiStreamReview(m.ID, id)
	return nil
}

// tuiStreamReview follows a running review and mirrors its text into the TUI.
// The job broker hands over everything streamed so far on subscribe, so no
// delta is lost to the gap between starting the review and subscribing.
func tuiStreamReview(matchID, reviewID string) {
	job := lookupJob(reviewID)
	if job == nil {
		tuiFinishReviewFromStore(matchID, reviewID)
		return
	}
	history, ch, finished := job.subscribe()
	if finished {
		tuiFinishFromJob(matchID, reviewID, job, history)
		return
	}
	SetTUIReview(matchID, history, true)
	text := history
	for delta := range ch {
		text += delta
		SetTUIReview(matchID, text, true)
	}
	tuiFinishFromJob(matchID, reviewID, job, text)
}

// tuiFinishFromJob finalises a review from the in-flight job rather than the
// store. The job closes its subscriber channels just before it writes the
// result, so reading the store here could briefly see the pre-run record and
// blank text the operator has already watched stream in. The job always has the
// authoritative streamed text and the terminal error message.
func tuiFinishFromJob(matchID, reviewID string, job *reviewJob, streamed string) {
	full, _, errMsg := job.result()
	text := full
	if strings.TrimSpace(text) == "" {
		text = streamed
	}
	if msg := strings.TrimSpace(errMsg); msg != "" {
		if strings.TrimSpace(text) != "" {
			// A truncated answer is still worth showing, with the reason after it.
			text += "\n\n" + msg
		} else {
			text = "review failed: " + msg
		}
	}
	if strings.TrimSpace(text) == "" {
		// Nothing was produced at all: fall back to the stored record, which may
		// carry an error the job did not.
		tuiFinishReviewFromStore(matchID, reviewID)
		return
	}
	SetTUIReview(matchID, text, false)
}

// tuiFinishReviewFromStore replaces the streamed text with the stored result,
// which also carries the failure message when the review did not complete.
func tuiFinishReviewFromStore(matchID, reviewID string) {
	if reviewStore == nil {
		SetTUIReview(matchID, "", false)
		return
	}
	rev, ok := reviewStore.Get(reviewID)
	if !ok {
		SetTUIReview(matchID, "", false)
		return
	}
	text := strings.TrimSpace(rev.Assessment)
	if text == "" && strings.TrimSpace(rev.Error) != "" {
		text = "review failed: " + strings.TrimSpace(rev.Error)
	}
	SetTUIReview(matchID, text, false)
}
