# AGENTS.md

## Learned User Preferences
- Make surgical, minimal, backward-compatible changes; never touch out-of-scope files (cmd/shhgit/web.go, cmd/shhgit/tui.go, cmd/shhgit/dashboard/) even if they appear broken.
- Verify every change with build + vet + targeted tests + full test suite + an end-to-end binary run; report files changed, what was broken/fixed, and test/build output.
- Terminal output must degrade cleanly when piped (no ANSI/OSC-8 escapes); honor NO_COLOR and SHHGIT_NO_LINKS.
- Never echo whole secrets into logs: truncate long/PEM matches (keep the head, drop the tail) instead of replacing them with a redaction notice.
- Suppress noise by default: no success logs for webhook sends or file writes; webhook trouble is reported once per run, in red.
- Do not report placeholder/template values (`<password>`, `${TOKEN}`, `YOUR_KEY`) as findings, and never flag a file on its name alone.
- Do not commit or stage changes unless explicitly asked.
- Keep log output CI-friendly: minimal preset = exactly one line per finding.
- The recurring "FIND AND FIX BUGS" loop task explicitly authorizes whole-codebase changes (overrides the web.go/tui.go/dashboard exclusion for genuine bug fixes); keep changes surgical and backward-compatible.

## Learned Workspace Facts
- Repo: shhgit secret scanner (Go module github.com/trebor048/shhgot) at C:\Users\me\GIT\shhgit2\shhgit; default mode is terminal-only, with web and tui modes and 5 logFormat presets (minimal, fancy, ultra fancy, even more fancy, neon).
- Layout: the binary is `package main` under `cmd/shhgit/` (the dashboard is embedded from `cmd/shhgit/dashboard/index.html`); shared libraries are `core/`, `aiproviders/`, `reviewstore/`.
- Concurrent sibling agents edit this working tree; transient build failures (e.g., cmd/shhgit/tui.go) resolve on their own — verify before assuming your change broke the build.
- Verification commands for this repo: go build -o shhgit-test.exe ./cmd/shhgit, go vet ./..., go test -run 'TestLog|TestSecret|TestLink|TestFile|TestLocal|TestEntropy|TestSignature' -v ./cmd/shhgit, go test ./....
- Tests run with the package directory as CWD, so a test that needs a repo-root file must resolve it from runtime.Caller, not a bare relative path.
- Environment: Windows with pwsh; use $env:TEMP for scratch harnesses; run gofmt -w after edits (cmd/shhgit/main.go was not gofmt-clean after editing).
- The rtk wrapper (rtk go build/vet) is used for token savings; it prints a "[rtk] No hook installed" notice but works.
- config.yaml and ai_review/settings.json hold real secrets and are gitignored — never commit, delete, or rotate them; config.yaml.example is the safe tracked template.
- PowerShell: `gofmt -w cmd/shhgit/*.go` does not glob-expand — use `Get-ChildItem cmd\shhgit -Filter *.go | ForEach-Object { gofmt -w $_.FullName }`.
- `cmd/shhgit/dashboard/index.html` must be searched with Select-String (grep tools fail on it); Node helper scripts need the .cjs extension.
- Windows Defender excludes C:\Users\me\GIT\shhgit2\shhgit and I:\go-tmp (the binary is otherwise flagged PUA:Win32/Vigua.A).
- Full verification gate: build + vet + go test ./... (386) + `$env:CGO_ENABLED='1'; go test -race -count=1 ./core/ ./cmd/shhgit/ ./aiproviders/` (344) + deadcode (0 unreachable) + a --local smoke scan against a scratch dir in $env:TEMP (expect exit 1 + a [SECRET] line).
- The web server's /api/events SSE endpoint holds the stream open indefinitely — a shell check against it times out by design, not a hang.
- Background swarm subagents have been unreliable (return no reports); the task/loop agent path works — prefer it for delegated analysis.