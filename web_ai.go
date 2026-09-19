package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/trebor048/shhgot/aireview"
	"github.com/trebor048/shhgot/core"
	git "gopkg.in/src-d/go-git.v4"
)

var aiSvc *aireview.Service

// realClone performs a shallow, depth-1 clone of a public repo.
func realClone(dst, repoURL string) error {
	_, err := git.PlainClone(dst, false, &git.CloneOptions{
		URL:      repoURL,
		Depth:    1,
		Progress: nil,
	})
	return err
}

// InitAIReview wires the AI review service from config. Called once from
// main.go before StartWebServer. Non-fatal on error (logs and continues).
func InitAIReview(cfg *core.Config) error {
	root := "ai_review"
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	// compile the format-check signatures from config
	var sigs []aireview.Sig
	for _, s := range cfg.Signatures {
		re, err := regexp.Compile(s.Regex)
		if err != nil {
			continue
		}
		sigs = append(sigs, aireview.Sig{Name: s.Name, Re: re})
	}
	gate := aireview.NewGate(
		cfg.BlacklistedStrings,
		nil, // known exact test keys live in config.BlacklistedStrings already
		sigs,
		aireview.NewProviderVerifier(nil),
	)
	svc, err := aireview.NewService(root, cfg.Webhook, dashboardBaseURL(), realClone, gate)
	if err != nil {
		return err
	}
	svc.SetOnEvent(func(j *aireview.Job) {
		broadcastFeed(FeedEvent{Type: "ai", AI: j})
	})
	svc.StartWatcher(3 * time.Second)
	aiSvc = svc
	log.Printf("[web] AI review ready (root=%s, webhook=%s)", root, boolStr(cfg.Webhook != ""))
	return nil
}

func boolStr(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func dashboardBaseURL() string {
	return "http://127.0.0.1:8080"
}

// registerAIRoutes registers the /api/ai/* endpoints.
func registerAIRoutes() {
	// These routes return plaintext detected secrets (Job.Secret) in their list,
	// detail and archive responses, so they carry the same loopback guard the AI
	// review chat routes use instead of relying on CORS alone.
	http.HandleFunc("/api/ai/flag", corsMiddleware(aiLocalGuard(aiFlag)))
	http.HandleFunc("/api/ai/jobs", corsMiddleware(aiLocalGuard(aiJobs)))
	http.HandleFunc("/api/ai/jobs/", corsMiddleware(aiLocalGuard(aiJobPath)))
	http.HandleFunc("/api/ai/jobs/archive/", corsMiddleware(aiLocalGuard(aiArchiveDownload)))
	http.HandleFunc("/api/ai/cases", corsMiddleware(aiLocalGuard(aiCases)))
	http.HandleFunc("/api/ai/notifications", corsMiddleware(aiLocalGuard(aiNotifications)))
	http.HandleFunc("/api/ai/notifications/read", corsMiddleware(aiLocalGuard(aiNotificationsRead)))
}

func requireAI() (*aireview.Service, error) {
	if aiSvc == nil {
		return nil, fmt.Errorf("AI review not initialized")
	}
	return aiSvc, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// POST /api/ai/flag
func aiFlag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	svc, err := requireAI()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	var body struct {
		RepoURL      string `json:"repo_url"`
		File         string `json:"file"`
		Signature    string `json:"signature"`
		Secret       string `json:"secret"`
		MatchContext string `json:"match_context"`
		Stars        int    `json:"stars"`
		Authorized   bool   `json:"authorized"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	j, err := svc.Flag(body.RepoURL, body.File, body.Signature, body.Secret, body.MatchContext, body.Stars, body.Authorized)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": j.ID, "case_id": j.CaseID, "state": string(j.State)})
}

// GET /api/ai/jobs
func aiJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	svc, err := requireAI()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	jobs, err := svc.Jobs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

// GET/POST /api/ai/jobs/<id>[/<action>]  (action: start|pause|resume|stop|archive)
func aiJobPath(w http.ResponseWriter, r *http.Request) {
	svc, err := requireAI()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/ai/jobs/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeErr(w, http.StatusBadRequest, "missing job id")
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		j, err := svc.Jobs.Get(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "job not found")
			return
		}
		logs, _ := svc.Jobs.ReadLogTail(id, 200)
		resultJSON, _ := os.ReadFile(filepath.Join("ai_review", "jobs", id, "result.json"))
		resultMD, _ := os.ReadFile(filepath.Join("ai_review", "jobs", id, "result.md"))
		var evidence []string
		evDir := filepath.Join("ai_review", "jobs", id, "evidence")
		if entries, err := os.ReadDir(evDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					evidence = append(evidence, e.Name())
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"job":       j,
			"logs_tail": logs,
			"result": map[string]any{
				"json":     string(resultJSON),
				"md":       string(resultMD),
				"evidence": evidence,
			},
		})
		return
	}
	action := parts[1]
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	switch action {
	case "start":
		err = svc.Start(id)
	case "pause":
		err = svc.Pause(id)
	case "resume":
		err = svc.Resume(id)
	case "stop":
		err = svc.Stop(id)
	case "archive":
		var zipPath string
		zipPath, err = svc.Archive(id)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]string{"archive": zipPath})
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "unknown action "+action)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true", "id": id, "action": action})
}

// GET /api/ai/jobs/archive/<id> (download zip)
func aiArchiveDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	if _, err := requireAI(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/ai/jobs/archive/")
	if rest == "" || strings.Contains(rest, "/") {
		writeErr(w, http.StatusBadRequest, "bad path")
		return
	}
	id := rest
	zipPath := filepath.Join("ai_review", "archive", id+".zip")
	if _, err := os.Stat(zipPath); err != nil {
		writeErr(w, http.StatusNotFound, "archive not found")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+id+".zip")
	http.ServeFile(w, r, zipPath)
}

// GET /api/ai/cases
func aiCases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	svc, err := requireAI()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	cases, err := svc.Cases.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cases)
}

// GET /api/ai/notifications
func aiNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	svc, err := requireAI()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	list, err := svc.Notify.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	unread, _ := svc.Notify.UnreadCount()
	writeJSON(w, http.StatusOK, map[string]any{"notifications": list, "unread": unread})
}

// POST /api/ai/notifications/read
func aiNotificationsRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	svc, err := requireAI()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := svc.Notify.MarkAllRead(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
