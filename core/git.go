package core

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
)

type GitResourceType int

const (
	LOCAL_SOURCE GitResourceType = iota
	GITHUB_SOURCE
	GITHUB_COMMENT
	GIST_SOURCE
	BITBUCKET_SOURCE
	GITLAB_SOURCE
	// GITHUB_SEARCH_SOURCE is a file discovered through GitHub code search
	// (the dork source), as opposed to a clone of the repository.
	GITHUB_SEARCH_SOURCE
	// PASTEBIN_SOURCE is a paste body discovered through the Pastebin
	// Scraping API source.
	PASTEBIN_SOURCE
	// SEARCHCODE_SOURCE is a searchcode.com code-search hit.
	SEARCHCODE_SOURCE
	// SOURCEGRAPH_SOURCE is a match found through Sourcegraph stream search.
	SOURCEGRAPH_SOURCE
	// GITHUB_COMMITS_SOURCE is a match found in a commit message through
	// GitHub commit search.
	GITHUB_COMMITS_SOURCE
	// GITHUB_ISSUES_SOURCE is a match found in an issue/PR title or body through
	// GitHub issue search.
	GITHUB_ISSUES_SOURCE
	// GREPAPP_SOURCE is a grep.app code-search hit.
	GREPAPP_SOURCE
	// PSBDMP_SOURCE is a Pastebin paste body discovered through psbdmp.ws.
	PSBDMP_SOURCE
)

type GitResource struct {
	Id   int64
	Type GitResourceType
	Url  string
	Ref  string
}

// Comment carries a scanned comment body together with the URL that points
// back to the source issue/PR, so matches can be traced to the exact comment.
type Comment struct {
	Body string
	Url  string
}

func CloneRepository(session *Session, url string, ref string, dir string, progress io.Writer) (*git.Repository, error) {
	seconds := *session.Options.CloneRepositoryTimeout
	if seconds == 0 {
		// A zero deadline is already expired, so every clone would abort; treat
		// 0 as the flag's 30s default rather than a "no timeout" request.
		seconds = 30
	}
	// Cap so the seconds->nanoseconds conversion cannot overflow time.Duration.
	const maxCloneSeconds = 1 << 31 // ~68 years
	if seconds > maxCloneSeconds {
		seconds = maxCloneSeconds
	}
	timeout := time.Duration(seconds) * time.Second
	// Derive from the session context so a shutdown/cancel aborts an in-flight
	// clone instead of leaving it to run out its own timeout.
	parent := session.Context
	if parent == nil {
		parent = context.Background()
	}
	localCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	session.Log.Debug("[%s] Cloning %s in to %s", url, ref, strings.Replace(dir, *session.Options.TempDirectory, "", -1))

	// scanning.clone_depth defaults to a shallow 1; an explicit positive value
	// overrides it. A full-history clone is never implied.
	depth := 1
	if session.Config != nil {
		if d := session.Config.Scanning.Int(session.Config.Scanning.CloneDepth, depth); d > 0 {
			depth = d
		}
	}

	// Prefer a native git binary when one is present: it is markedly faster and
	// lighter than the pure-Go implementation, which buffers objects in memory.
	// This is the single biggest remaining cost of a scan on a laptop.
	useNative := true
	if session.Config != nil {
		useNative = session.Config.Scanning.Bool(session.Config.Scanning.UseNativeGit, true)
	}
	if useNative && cloneNative(localCtx, url, ref, dir, depth, progress) {
		// The caller discards the handle and reads the checkout from disk, so
		// there is nothing to open here; returning nil also avoids go-git's
		// shallow-tree handling entirely.
		return nil, nil
	}
	// A failed or partial native clone must not leave a directory behind for
	// go-git to trip over.
	if err := os.RemoveAll(dir); err != nil {
		session.Log.Debug("[%s] cleanup: could not remove stale clone dir %s: %s", url, dir, err)
	}

	opts := &git.CloneOptions{
		Depth:             depth,
		RecurseSubmodules: git.NoRecurseSubmodules,
		URL:               url,
		SingleBranch:      true,
		Tags:              git.NoTags,
		Progress:          progress,
	}

	if ref != "" {
		opts.ReferenceName = plumbing.ReferenceName(ref)
	}

	repository, err := git.PlainCloneContext(localCtx, dir, false, opts)

	if err != nil {
		session.Log.Debug("[%s] Cloning failed: %s", url, err.Error())
		return nil, err
	}

	return repository, nil
}

// cloneNative clones with the git binary when one is on PATH, returning true
// only when the clone fully succeeded into dir. Any failure (no git, unknown
// ref, network error) falls back to the pure-Go path, so behaviour is unchanged
// where git is unavailable.
func cloneNative(ctx context.Context, url, ref, dir string, depth int, progress io.Writer) bool {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return false
	}
	// `git clone` refuses to write into an existing directory.
	if _, err := os.Stat(dir); err == nil {
		if err := os.RemoveAll(dir); err != nil {
			session.Log.Debug("[%s] cleanup: could not remove stale clone dir %s: %s", url, dir, err)
		}
	}

	args := []string{"clone", "--quiet", "--no-tags", "--single-branch"}
	if depth > 0 {
		args = append(args, "--depth", strconv.Itoa(depth))
	}
	if branch := branchName(ref); branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", url, dir)

	cmd := exec.CommandContext(ctx, gitPath, args...)
	// Never prompt: the scan is unattended and a credential prompt would hang it.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	if progress != nil {
		cmd.Stderr = progress
	} else {
		cmd.Stderr = io.Discard
	}
	cmd.Stdout = io.Discard

	return cmd.Run() == nil
}

// branchName turns a stored ref into what `git clone --branch` accepts: a short
// branch or tag name, not a fully qualified ref.
func branchName(ref string) string {
	ref = strings.TrimSpace(ref)
	for _, p := range []string{"refs/heads/", "refs/tags/"} {
		if strings.HasPrefix(ref, p) {
			return strings.TrimPrefix(ref, p)
		}
	}
	return ref
}
