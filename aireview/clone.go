package aireview

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxCloneFresh is how long a pooled clone is reused before re-cloning.
const MaxCloneFresh = 24 * time.Hour

// CloneFn clones the repo at url into dst. Injected so tests can use a fake.
type CloneFn func(dst, url string) error

// ClonePool manages shared shallow clones under root/clones/<owner>-<repo>/.
type ClonePool struct {
	root    string
	cloneFn CloneFn
}

// NewClonePool creates the pool rooted at root/clones.
func NewClonePool(root string, cloneFn CloneFn) *ClonePool {
	return &ClonePool{root: filepath.Join(root, "clones"), cloneFn: cloneFn}
}

// slugFromURL converts https://github.com/owner/repo(.git) to owner-repo.
func slugFromURL(repoURL string) (string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("cannot derive owner/repo from %q", repoURL)
	}
	name := strings.TrimSuffix(parts[len(parts)-1], ".git")
	return parts[len(parts)-2] + "-" + name, nil
}

// Ensure returns the clone path for repoURL, cloning when absent or stale.
// reused=true means an existing fresh clone was used without re-cloning.
func (p *ClonePool) Ensure(repoURL string) (path string, reused bool, err error) {
	slug, err := slugFromURL(repoURL)
	if err != nil {
		return "", false, err
	}
	dir := filepath.Join(p.root, slug)
	marker := filepath.Join(dir, ".cloned")
	if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) <= MaxCloneFresh {
		return dir, true, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	if err := p.cloneFn(dir, repoURL); err != nil {
		return "", false, err
	}
	f, err := os.Create(marker)
	if err != nil {
		return "", false, err
	}
	f.Close()
	return dir, false, nil
}
