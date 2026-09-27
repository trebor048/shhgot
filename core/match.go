package core

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type MatchFile struct {
	Path      string
	Filename  string
	Extension string
	Contents  []byte
	Size      int64  // Add file size
	Repo      string // Repository the file belongs to, set by the scan loop
	loaded    bool   // Track if contents are loaded

	// lower caches Contents lowercased for case-insensitive signature prefilters.
	// It is a pointer so the by-value copies MatchFile undergoes when it is
	// passed to every signature still share one cache, lowering each file once.
	lower *lowerCache
}

type lowerCache struct {
	once sync.Once
	buf  []byte
}

// EnableLowerCache arranges for LowerContents to be computed at most once for
// this file and shared across the copies made when it is handed to each
// signature. Callers that never need the lowercased form can skip it.
func (m *MatchFile) EnableLowerCache() { m.lower = &lowerCache{} }

// LowerContents returns Contents lowercased, matching the ASCII-only folding
// used by requiredLiterals. With the cache enabled it is computed once per file
// however many signatures consult it.
func (m *MatchFile) LowerContents() []byte {
	if m.lower == nil {
		return bytes.ToLower(m.Contents)
	}
	m.lower.once.Do(func() { m.lower.buf = bytes.ToLower(m.Contents) })
	return m.lower.buf
}

// IsBinaryContent reports whether content looks like a binary file: a NUL byte
// within the first few KB. Signatures are text patterns, so scanning such a file
// (images, compiled artifacts, vendored blobs) is pure cost.
func IsBinaryContent(b []byte) bool {
	const sniff = 8 * 1024
	if len(b) > sniff {
		b = b[:sniff]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// NewMatchFile creates a MatchFile with lazy content loading
func NewMatchFile(path string) MatchFile {
	path = filepath.ToSlash(path)
	_, filename := filepath.Split(path)
	extension := filepath.Ext(path)

	// Get file size WITHOUT reading contents. A stat failure leaves the size
	// unknown (-1) rather than 0, so GetContents reads the file bounded by the
	// configured cap instead of mistaking "unknown" for "empty".
	stat, err := os.Stat(path)
	size := int64(-1)
	if err == nil {
		size = stat.Size()
	}

	return MatchFile{
		Path:      path,
		Filename:  filename,
		Extension: extension,
		Contents:  nil, // Don't load yet
		Size:      size,
		loaded:    false,
	}
}

// GetContents lazily loads file contents only when needed.
//
// It sniffs the first few KB before reading the rest: a NUL byte there marks a
// binary file, which cannot hold a text secret, so the remainder is never read.
// On a repository full of images and vendored blobs that is the difference
// between reading the tree and reading the text in it.
func (m *MatchFile) GetContents() []byte {
	if m.loaded {
		return m.Contents
	}
	m.loaded = true

	// Check max file size before reading
	maxSize := int64(*session.Options.MaximumFileSize * 1024)
	if m.Size > maxSize {
		m.Contents = []byte{}
		return m.Contents
	}

	file, err := os.Open(m.Path)
	if err != nil {
		m.Contents = []byte{}
		return m.Contents
	}
	defer file.Close()

	const sniff = 8 * 1024
	head := make([]byte, sniff)
	hn, herr := io.ReadFull(file, head)
	if herr != nil && herr != io.ErrUnexpectedEOF && herr != io.EOF {
		m.Contents = []byte{}
		return m.Contents
	}
	head = head[:hn]
	// Binary files are skipped by default (scanning.scan_binary re-enables it):
	// text signatures cannot match them, so the rest of the read is wasted.
	scanBinary := session != nil && session.Config != nil &&
		session.Config.Scanning.Bool(session.Config.Scanning.ScanBinary, false)
	if !scanBinary && IsBinaryContent(head) {
		m.Contents = []byte{}
		return m.Contents
	}

	// The size is unknown when os.Stat failed while the MatchFile was built;
	// read the remainder up to the cap instead of assuming the file is empty.
	if m.Size < 0 {
		rest, _ := io.ReadAll(io.LimitReader(file, maxSize+1-int64(len(head))))
		m.Contents = append(head, rest...)
		if int64(len(m.Contents)) > maxSize {
			m.Contents = []byte{}
		}
		return m.Contents
	}

	// Pre-allocate the exact size and stitch the already-read head into it.
	m.Contents = make([]byte, m.Size)
	copied := copy(m.Contents, head)
	if copied < len(m.Contents) {
		n, rerr := io.ReadFull(file, m.Contents[copied:])
		if rerr != nil && rerr != io.ErrUnexpectedEOF {
			m.Contents = []byte{}
			return m.Contents
		}
		// The file shrank between os.Stat and the read. io.ReadFull reports
		// io.ErrUnexpectedEOF and leaves the rest of the pre-allocated slice as
		// zero bytes; trimming to what was actually read keeps the caller from
		// scanning NUL padding that is not in the file.
		m.Contents = m.Contents[:copied+n]
	}

	return m.Contents
}

// A blacklisted path entry is matched by shape, so a rule cannot quietly cover
// a sibling directory. "build/" matches the path component "build" (so
// "a/build/x" but not "a/rebuild/x"), "vendor/autoload.php" matches that
// component run, and "Dockerfile" or "*.bak" match the file's base name.
type pathRuleKind int

const (
	pathRuleComponent pathRuleKind = iota // one component, possibly a glob
	pathRuleFragment                      // a run of components inside the path
	pathRuleBasename                      // the file's base name
)

type pathRule struct {
	kind    pathRuleKind
	pattern string
}

// Pre-compute blacklist checks for faster lookup
var (
	blacklistExtMap   map[string]bool
	blacklistPathList []pathRule
	blacklistOnce     sync.Once
)

func initBlacklists() {
	blacklistOnce.Do(func() {
		blacklistExtMap = make(map[string]bool, len(session.Config.BlacklistedExtensions))
		for _, ext := range session.Config.BlacklistedExtensions {
			blacklistExtMap[strings.ToLower(ext)] = true
		}

		seen := make(map[pathRule]bool, len(session.Config.BlacklistedPaths))
		for _, raw := range session.Config.BlacklistedPaths {
			// Normalise to forward slashes: the path under test is run through
			// filepath.ToSlash for the same reason, so {sep} works on every OS.
			entry := strings.TrimSpace(strings.Replace(raw, "{sep}", "/", -1))
			if entry == "" {
				continue
			}
			trimmed := strings.TrimSuffix(entry, "/")
			var rule pathRule
			switch {
			case strings.Contains(trimmed, "/"):
				rule = pathRule{pathRuleFragment, entry}
			case strings.HasSuffix(entry, "/"):
				rule = pathRule{pathRuleComponent, trimmed}
			default:
				rule = pathRule{pathRuleBasename, entry}
			}
			if seen[rule] {
				continue
			}
			seen[rule] = true
			blacklistPathList = append(blacklistPathList, rule)
		}
	})
}

func IsSkippableFile(path string) bool {
	initBlacklists()

	slashed := filepath.ToSlash(path)
	// Fast map lookup instead of linear search
	if blacklistExtMap[strings.ToLower(filepath.Ext(slashed))] {
		return true
	}

	components := strings.Split(slashed, "/")
	base := components[len(components)-1]
	for _, rule := range blacklistPathList {
		switch rule.kind {
		case pathRuleComponent:
			for _, c := range components {
				if ok, _ := filepath.Match(rule.pattern, c); ok {
					return true
				}
			}
		case pathRuleFragment:
			if strings.Contains(slashed, rule.pattern) {
				return true
			}
		case pathRuleBasename:
			if ok, _ := filepath.Match(rule.pattern, base); ok {
				return true
			}
		}
	}

	return false
}

func (match MatchFile) CanCheckEntropy() bool {
	if match.Filename == "id_rsa" {
		return false
	}

	for _, skippableExt := range session.Config.BlacklistedEntropyExtensions {
		if match.Extension == skippableExt {
			return false
		}
	}

	return true
}

// ScanPlan is what a scan of a directory would do, computed without opening a
// single file. It backs --dry-run, so the size caps and blacklists can be tuned
// against a real repository instead of guessed at.
type ScanPlan struct {
	Total       int
	Scannable   int
	SkippedExt  int
	SkippedPath int
	SkippedSize int
}

// PlanScan walks dir and classifies each file without reading it.
func PlanScan(dir string) (ScanPlan, error) {
	initBlacklists()
	maxFileSize := *session.Options.MaximumFileSize * 1024

	var plan ScanPlan
	err := filepath.Walk(dir, func(path string, f os.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		if f.IsDir() {
			return nil
		}
		plan.Total++
		switch {
		case uint(f.Size()) > maxFileSize:
			plan.SkippedSize++
		case IsSkippableFile(path):
			if blacklistExtMap[strings.ToLower(filepath.Ext(filepath.ToSlash(path)))] {
				plan.SkippedExt++
			} else {
				plan.SkippedPath++
			}
		default:
			plan.Scannable++
		}
		return nil
	})
	return plan, err
}

// GetMatchingFiles returns the scannable files under dir. Callers that also
// need the total file count should use GetMatchingFilesCounted so the tree is
// walked once.
func GetMatchingFiles(dir string) []MatchFile {
	files, _ := GetMatchingFilesCounted(dir)
	return files
}

// GetMatchingFilesCounted walks dir once and returns the scannable files plus
// the total number of files seen (including the blacklisted and oversized ones
// this slice drops). The count used to come from a second full traversal of the
// same tree.
func GetMatchingFilesCounted(dir string) ([]MatchFile, int) {
	fileList := make([]MatchFile, 0, 1000) // Pre-allocate with reasonable capacity
	maxFileSize := *session.Options.MaximumFileSize * 1024

	initBlacklists() // Ensure blacklists are ready

	total := 0
	if err := filepath.Walk(dir, func(path string, f os.FileInfo, err error) error {
		// Fast rejection checks first (cheapest to most expensive)
		if err != nil {
			return nil
		}
		if f.IsDir() {
			return nil
		}
		total++
		if uint(f.Size()) > maxFileSize {
			return nil
		}
		if IsSkippableFile(path) {
			return nil
		}

		fileList = append(fileList, NewMatchFile(path))
		return nil
	}); err != nil {
		session.Log.Debug("Error walking %s: %s", dir, err)
	}

	return fileList, total
}
