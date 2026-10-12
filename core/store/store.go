package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

var (
	ErrClosed   = errors.New("store: closed")
	ErrNotFound = errors.New("store: not found")
	ErrDecrypt  = errors.New("store: decryption failed (wrong passphrase or corrupted file)")
)

type Record struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	Timestamp   time.Time `json:"timestamp"`
	Source      string    `json:"source"`
	URL         string    `json:"url"`
	File        string    `json:"file"`
	Signature   string    `json:"signature"`
	Matches     []string  `json:"matches"`
	Secret      string    `json:"secret,omitempty"`
	Line        int       `json:"line,omitempty"`
	Stars       int       `json:"stars"`
	Priority    int       `json:"priority"`
	// Severity, Provider, Tier and Balance are the dynamic-severity metadata
	// added in Slice 6. They are optional: a record written before this slice
	// (or one whose secret was never verified) leaves them empty, and every
	// consumer must tolerate that.
	Severity string `json:"severity,omitempty"`
	Provider string `json:"provider,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Balance  string `json:"balance,omitempty"`
}

type Filter struct {
	Source    string
	Signature string
	Since     time.Time
	Until     time.Time
	Limit     int
}

type Config struct {
	Path             string
	DedupeTTL        time.Duration
	MaxDedupeEntries int
	CompactAfter     int
	Enabled          bool
	Now              func() time.Time
}

type Store interface {
	Seen(fp string) bool
	Append(Record) error
	List(Filter) ([]Record, error)
	SaveCursor(name string, v any) error
	LoadCursor(name string, out any) error
	Compact() error
	Close() error
	Degraded() bool
}

// fingerprintSeparator delimits the fields hashed into a fingerprint. It cannot
// appear in any field, so boundary-shifted values (repo ".../a" + file "b.py"
// versus repo ".../ab" + file ".py") never collapse onto the same input.
const fingerprintSeparator = "\x00"

func Fingerprint(signature, source, match, repo, file string) string {
	sum := sha256.Sum256([]byte(signature + fingerprintSeparator + source +
		fingerprintSeparator + match + fingerprintSeparator + repo +
		fingerprintSeparator + file))
	return hex.EncodeToString(sum[:])
}
