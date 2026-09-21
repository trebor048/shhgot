package core

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

func GetTempDir(suffix string) string {
	dir := filepath.Join(*session.Options.TempDirectory, suffix)

	// Clear any previous contents, then make sure the directory exists.
	//
	// This used to MkdirAll only on the not-exist path and RemoveAll on the other,
	// so the second call for the same suffix (a repeated comment body, a re-scan
	// of the same repo) removed the directory and handed the caller back a path
	// that was gone. The clone helper recreates its target, but processComment
	// writes a file into it and ignored the failure, so those comments were never
	// scanned.
	os.RemoveAll(dir)
	os.MkdirAll(dir, os.ModePerm)

	return dir
}

func PathExists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}

	if os.IsNotExist(err) {
		return false
	}

	return false
}

// LogIfError reports a non-fatal error. It deliberately does NOT call GetSession:
// it is reached from inside GetSession's own sync.Once (Session.Start calls
// InitCsvWriter), and sync.Once.Do is not reentrant, so the nested call blocked
// forever - an unwritable --csv-path hung the process at startup instead of
// saying why. The session logger is used once it exists, stderr before that.
func LogIfError(text string, err error) {
	if err == nil {
		return
	}
	if session != nil && session.Log != nil {
		session.Log.Error("%s (%s)", text, err.Error())
		return
	}
	fmt.Fprintf(os.Stderr, "%s (%s)\n", text, err.Error())
}

func GetHash(s string) string {
	h := sha1.New()
	h.Write([]byte(s))

	return hex.EncodeToString(h.Sum(nil))
}

func Pluralize(count int, singular string, plural string) string {
	if count == 1 {
		return singular
	}

	return plural
}

func GetEntropy(data string) (entropy float64) {
	if data == "" {
		return 0
	}

	// Count raw bytes. The previous implementation used
	// strings.Count(data, string(byte(i))), and for i >= 0x80 string(byte(i))
	// is the UTF-8 encoding of a rune (two bytes), not the single byte, so the
	// frequency of every high byte was wrong: it looked for the 2-byte sequence
	// instead of the byte. A histogram over the bytes is both correct and faster.
	var counts [256]int
	for i := 0; i < len(data); i++ {
		counts[data[i]]++
	}

	length := float64(len(data))
	for _, count := range counts {
		if count == 0 {
			continue
		}
		px := float64(count) / length
		entropy += -px * math.Log2(px)
	}

	return entropy
}
