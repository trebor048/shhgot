# AGENTS.md

## Learned User Preferences
- Make surgical, minimal, backward-compatible changes; never touch out-of-scope files (web.go, tui.go, dashboard/) even if they appear broken.
- Verify every change with build + vet + targeted tests + full test suite + an end-to-end binary run; report files changed, what was broken/fixed, and test/build output.
- Terminal output must degrade cleanly when piped (no ANSI/OSC-8 escapes); honor NO_COLOR and SHHGIT_NO_LINKS.
- Never echo whole secrets into logs: truncate long/PEM matches (keep the head, drop the tail) instead of replacing them with a redaction notice.
- Suppress noise by default: no success logs for webhook sends or file writes; webhook trouble is reported once per run, in red.
- Do not report placeholder/template values (`<password>`, `${TOKEN}`, `YOUR_KEY`) as findings, and never flag a file on its name alone.
- Do not commit or stage changes unless explicitly asked.
- Keep log output CI-friendly: minimal preset = exactly one line per finding.

## Learned Workspace Facts
- Repo: shhgit secret scanner (Go module github.com/trebor048/shhgot) at C:\Users\me\GIT\shhgit2\shhgit; default mode is terminal-only, with web and tui modes and 5 logFormat presets (minimal, fancy, ultra fancy, even more fancy, neon).
- Concurrent sibling agents edit this working tree; transient build failures (e.g., tui.go) resolve on their own — verify before assuming your change broke the build.
- Working tree has pre-existing uncommitted changes (aiproviders/, modes.go, tui.go, web_review.go, go.mod/go.sum) that must be left untouched.
- Verification commands for this repo: go build -o shhgit-test.exe ., go vet ./..., go test -run 'TestLog|TestSecret|TestLink|TestFile|TestLocal|TestEntropy|TestSignature' -v ., go test ./....
- Environment: Windows with pwsh; use $env:TEMP for scratch harnesses; run gofmt -w after edits (main.go was not gofmt-clean after editing).
- The rtk wrapper (rtk go build/vet) is used for token savings; it prints a "[rtk] No hook installed" notice but works.