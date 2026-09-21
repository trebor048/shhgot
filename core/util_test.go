package core

import (
	"math"
	"testing"
)

// GetEntropy used to count occurrences with strings.Count(data, string(byte(i))).
// For i >= 0x80 string(byte(i)) is the two-byte UTF-8 encoding of a rune, not
// the raw byte, so high bytes were never counted. A string of 128 distinct high
// bytes therefore reported entropy 0 instead of 7 bits.
func TestGetEntropyCountsHighBytes(t *testing.T) {
	high := make([]byte, 128)
	for i := range high {
		high[i] = byte(0x80 + i)
	}
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}

	cases := []struct {
		name string
		data string
		want float64
	}{
		{"empty", "", 0},
		{"single symbol", "aaaaaaaa", 0},
		{"four distinct", "abcd", 2},
		{"128 distinct high bytes", string(high), 7},
		{"256 distinct bytes", string(allBytes), 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetEntropy(tc.data)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("GetEntropy(%q) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}
