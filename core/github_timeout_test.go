package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-github/github"
)

// A black-holed GetByID used to pin a worker indefinitely: the go-github client
// has no http.Client.Timeout and the call was handed session.Context, which is
// never cancelled. The call must give up on its own bound and return a timeout
// error rather than draining the client pool.
func TestGetRepositoryGivesUpOnAHangingServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	old := repoRequestTimeout
	repoRequestTimeout = 200 * time.Millisecond
	defer func() { repoRequestTimeout = old }()

	gh := github.NewClient(nil)
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	gh.BaseURL = base

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		Context:          ctx,
		Config:           &Config{GitHubAccessTokens: []string{"test-token"}},
		Log:              &Logger{},
		Clients:          make(chan *GitHubClientWrapper, 1),
		ExhaustedClients: make(chan *GitHubClientWrapper, 1),
		RemovedTokens:    map[string]bool{},
		Progress:         NewProgressManager(),
	}
	sess.Clients <- &GitHubClientWrapper{
		Client:           gh,
		Token:            "test-token",
		RateLimitedUntil: time.Now().Add(-time.Second),
	}

	done := make(chan error, 1)
	go func() {
		_, err := GetRepository(sess, 987654321)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("GetRepository error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		// Unblock the in-flight request so the server can shut down.
		cancel()
		t.Fatal("GetRepository did not give up within the timeout bound")
	}
}
