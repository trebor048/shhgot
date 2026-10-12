package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"golang.org/x/crypto/scrypt"
)

// validateExportPath resolves path to an absolute path and ensures it resides
// within the user's home directory (or STORE_BASE_DIR env var). This prevents
// path traversal via CLI arguments like "../../../etc/shhgit.enc".
func validateExportPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("export: resolve path: %w", err)
	}
	baseDir := os.Getenv("STORE_BASE_DIR")
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("export: get home dir: %w", err)
		}
		baseDir = home
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("export: resolve base dir: %w", err)
	}
	rel, err := filepath.Rel(absBase, abs)
	if err != nil || isOutsideBase(rel) {
		return "", fmt.Errorf("export: path %q escapes allowed base directory %q", abs, absBase)
	}
	return abs, nil
}

const (
	encSaltLen  = 16
	encNonceLen = 12
	encKeyLen   = 32
	scryptN     = 1 << 15
	scryptR     = 8
	scryptP     = 1
)

var encMagic = []byte("SHHSTORE")

var csvHeader = []string{
	"id", "fingerprint", "timestamp", "source", "url", "file",
	"signature", "matches", "secret", "line", "stars", "priority",
	"severity", "provider", "tier", "balance",
}

// csvSafe prefixes a leading formula/control character with a single quote so a
// spreadsheet cannot execute a value drawn from scanned repository content
// (CSV formula injection, CWE-1236). encoding/csv quotes delimiters but does not
// neutralise a cell beginning with =, +, -, @, tab or CR.
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func ExportCSV(w io.Writer, recs []Record) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, r := range recs {
		matches, err := json.Marshal(r.Matches)
		if err != nil {
			return err
		}
		row := []string{
			csvSafe(r.ID),
			csvSafe(r.Fingerprint),
			csvSafe(r.Timestamp.Format(time.RFC3339Nano)),
			csvSafe(r.Source),
			csvSafe(r.URL),
			csvSafe(r.File),
			csvSafe(r.Signature),
			csvSafe(string(matches)),
			csvSafe(r.Secret),
			strconv.Itoa(r.Line),
			strconv.Itoa(r.Stars),
			strconv.Itoa(r.Priority),
			csvSafe(r.Severity),
			csvSafe(r.Provider),
			csvSafe(r.Tier),
			csvSafe(r.Balance),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func ExportJSONL(w io.Writer, recs []Record) error {
	enc := json.NewEncoder(w)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

func ExportEncrypted(path string, recs []Record, passphrase string) error {
	if passphrase == "" {
		return errors.New("store: empty passphrase")
	}
	absPath, err := validateExportPath(path)
	if err != nil {
		return err
	}
	var plain bytes.Buffer
	if err := ExportJSONL(&plain, recs); err != nil {
		return err
	}
	blob, err := encrypt(plain.Bytes(), passphrase)
	if err != nil {
		return err
	}
	// Write temp-and-rename, not bare WriteFile: a crash mid-write must leave
	// either the previous export or the complete new one, never a truncated
	// ciphertext. Mirrors the review/ai-provider stores.
	tmp, err := os.CreateTemp(filepath.Dir(absPath), ".export-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0600); err != nil && runtime.GOOS != "windows" {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, absPath); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	syncDirForFile(absPath)
	return nil
}

// syncDirForFile fsyncs the directory containing path so a just-completed
// rename is durable through power loss. Best-effort: not every
// platform/filesystem supports directory sync.
func syncDirForFile(path string) {
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

func DecryptFile(path, passphrase string) ([]Record, error) {
	absPath, err := validateExportPath(path)
	if err != nil {
		return nil, err
	}
	blob, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	plain, err := decrypt(blob, passphrase)
	if err != nil {
		return nil, err
	}
	return parseJSONL(plain)
}

func encrypt(plain []byte, passphrase string) ([]byte, error) {
	salt := make([]byte, encSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, encKeyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, encNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	header := make([]byte, 0, len(encMagic)+encSaltLen+encNonceLen)
	header = append(header, encMagic...)
	header = append(header, salt...)
	header = append(header, nonce...)
	return gcm.Seal(header, nonce, plain, header), nil
}

func decrypt(blob []byte, passphrase string) ([]byte, error) {
	minLen := len(encMagic) + encSaltLen + encNonceLen
	if len(blob) < minLen || !bytes.Equal(blob[:len(encMagic)], encMagic) {
		return nil, ErrDecrypt
	}
	header := blob[:minLen]
	salt := blob[len(encMagic) : len(encMagic)+encSaltLen]
	nonce := blob[len(encMagic)+encSaltLen : minLen]
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, encKeyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, blob[minLen:], header)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

func parseJSONL(data []byte) ([]Record, error) {
	var out []Record
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("store: parse record: %w", err)
		}
		out = append(out, r)
	}
	return out, nil
}
