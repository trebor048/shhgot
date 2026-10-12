package core

import "testing"

// TestSeverityColorHexMap pins the severity -> Discord colour mapping and the
// empty fallback for an unknown/absent severity.
func TestSeverityColorHexMap(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"critical", severityColorCritical},
		{"CRITICAL", severityColorCritical},
		{"high", severityColorHigh},
		{" high ", severityColorHigh},
		{"medium", severityColorMedium},
		{"low", severityColorLow},
		{"", ""},
		{"unknown", ""},
	}
	for _, c := range cases {
		if got := SeverityColorHex(c.in); got != c.want {
			t.Errorf("SeverityColorHex(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEmbedColorPrefersSeverityOverSignature proves the embed accent tracks the
// verified severity when one is known, and only falls back to the signature
// colour (used for pre-Slice-6 records) otherwise.
func TestEmbedColorPrefersSeverityOverSignature(t *testing.T) {
	const signatureColor = "#10A37F"

	// Every severity maps to a distinct decimal colour.
	seen := map[int]string{}
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		got := EmbedColor(sev, signatureColor)
		want := parseColorHex(SeverityColorHex(sev))
		if got != want {
			t.Errorf("EmbedColor(%q) = %d, want %d", sev, got, want)
		}
		if prev, ok := seen[got]; ok {
			t.Errorf("severity %q shares colour %d with %q", sev, got, prev)
		}
		seen[got] = sev
	}

	// A critical finding must not keep the (green) signature colour.
	if got := EmbedColor("critical", signatureColor); got == parseColorHex(signatureColor) {
		t.Errorf("critical severity kept the signature colour %d", got)
	}

	// An unknown severity falls back to the signature colour.
	if got, want := EmbedColor("", signatureColor), parseColorHex(signatureColor); got != want {
		t.Errorf("EmbedColor(empty) = %d, want signature colour %d", got, want)
	}
	if got, want := EmbedColor("bogus", signatureColor), parseColorHex(signatureColor); got != want {
		t.Errorf("EmbedColor(bogus) = %d, want signature colour %d", got, want)
	}
}
