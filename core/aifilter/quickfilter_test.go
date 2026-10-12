package aifilter

import "testing"

func TestQuickFilterTruthTable(t *testing.T) {
	const realisticKey = "sk-proj-K7mQ2vNp9Rt4Wx6Lz3Bd8Gh5Jc1Fa"
	const realisticToken = "ghp_9fK2mQ7vNp4Rt8Wx3Lz6Bd1Gh5Jc"

	cases := []struct {
		name      string
		candidate string
		context   string
		filePath  string
		suppress  bool
		reason    string
	}{
		{
			name:      "placeholder key in a test path",
			candidate: "sk-test-your-api-key-here",
			filePath:  "test.py",
			suppress:  true,
			reason:    reasonPlaceholder,
		},
		{
			name:      "your-api-key sentinel",
			candidate: "YOUR_API_KEY",
			filePath:  "config.py",
			suppress:  true,
			reason:    reasonPlaceholder,
		},
		{
			name:      "angle-bracket sentinel",
			candidate: "<API_KEY>",
			filePath:  "config.py",
			suppress:  true,
			reason:    reasonPlaceholder,
		},
		{
			name:      "x run",
			candidate: "xxxx",
			filePath:  "main.go",
			suppress:  true,
			reason:    reasonPlaceholder,
		},
		{
			name:      "a run",
			candidate: "aaaaaaaa",
			filePath:  "main.go",
			suppress:  true,
			reason:    reasonPlaceholder,
		},
		{
			name:      "documentation path",
			candidate: "AKIAIOSFODNN7REALKEY",
			filePath:  "README.md",
			suppress:  true,
			reason:    reasonDocPath,
		},
		{
			name:      "context marker",
			candidate: realisticKey,
			context:   "# example configuration\nOPENAI_API_KEY=" + realisticKey,
			filePath:  "config.py",
			suppress:  true,
			reason:    reasonContext,
		},
		{
			name:      "low entropy candidate",
			candidate: "zzzzzz",
			filePath:  "config.py",
			suppress:  true,
			reason:    reasonLowEntropy,
		},
		{
			name:      "repeating prefix",
			candidate: "q7q7q7K9mP2vN4xR6tW8",
			filePath:  "config.py",
			suppress:  true,
			reason:    reasonRepeating,
		},
		{
			name:      "realistic project key without markers is kept",
			candidate: realisticKey,
			filePath:  "config.py",
			suppress:  false,
		},
		{
			name:      "realistic github token without markers is kept",
			candidate: realisticToken,
			filePath:  "app.py",
			suppress:  false,
		},
		{
			name:      "non-marker context does not suppress",
			candidate: realisticKey,
			context:   "export const apiKey = " + realisticKey,
			filePath:  "src/config.py",
			suppress:  false,
		},
		{
			name:      "test directory path",
			candidate: realisticKey,
			filePath:  "tests/fixtures/config.py",
			suppress:  true,
			reason:    reasonDocPath,
		},
		{
			name:      "docs directory path",
			candidate: realisticKey,
			filePath:  "docs/setup.py",
			suppress:  true,
			reason:    reasonDocPath,
		},
		{
			name:      "contest substring is not a test path",
			candidate: realisticKey,
			filePath:  "src/contest/app.py",
			suppress:  false,
		},
		{
			name:      "attestation substring is not a test path",
			candidate: realisticKey,
			filePath:  "internal/attestation/keys.go",
			suppress:  false,
		},
		{
			name:      "latest substring is not a test path",
			candidate: realisticKey,
			filePath:  "latest/build.sh",
			suppress:  false,
		},
		{
			name:      "mockingbird substring is not a mock path",
			candidate: realisticKey,
			filePath:  "mockingbird.go",
			suppress:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := QuickFilter(tc.candidate, tc.context, tc.filePath)
			if got.Suppress != tc.suppress {
				t.Fatalf("QuickFilter(%q, %q, %q).Suppress = %v, want %v (reason %q)",
					tc.candidate, tc.context, tc.filePath, got.Suppress, tc.suppress, got.Reason)
			}
			if got.Reason != tc.reason {
				t.Fatalf("QuickFilter(%q, %q, %q).Reason = %q, want %q",
					tc.candidate, tc.context, tc.filePath, got.Reason, tc.reason)
			}
		})
	}
}
