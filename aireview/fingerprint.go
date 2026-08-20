package aireview

import (
	"crypto/sha256"
	"encoding/hex"
)

// Fingerprint returns the sha256 hex digest of the secret. Identical secrets
// always produce identical fingerprints; used for case-level deduplication.
func Fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
