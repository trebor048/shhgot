package core

import (
	"context"
	"io"
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
	timeout := time.Duration(*session.Options.CloneRepositoryTimeout) * time.Second
	localCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	session.Log.Debug("[%s] Cloning %s in to %s", url, ref, strings.Replace(dir, *session.Options.TempDirectory, "", -1))

	// scanning.clone_depth defaults to a shallow 1; an explicit positive value
	// overrides it. A full-history clone is never implied.
	depth := 1
	if session.Config != nil {
		depth = session.Config.Scanning.Int(session.Config.Scanning.CloneDepth, depth)
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
