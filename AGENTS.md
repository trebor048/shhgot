# AGENTS.md

## Workspace facts

- Go module path is `github.com/trebor048/shhgot`; the repo directory is `shhgit`. Import paths use `shhgot`, not `shhgit` (e.g. `github.com/trebor048/shhgot/core`).
- `cmd/shhgit/log.go` and `cmd/shhgit/progress.go` do not exist. Log/preset formatting lives in `cmd/shhgit/main.go`; scan progress is `core.GlobalScanProgress` in `core/scan_progress.go`.
- Mode is selected before `flag.Parse()`: terminal (default), `--web`, `--tui`. `cmd/shhgit/modes.go` scans and strips the mode flags from `os.Args`, so they are not registered with the flag package.
- `cmd/shhgit` tests include `log_format_test.go`, `tui_test.go`, `activity_test.go`, `mode_args_test.go`, `webui_test.go`, `web_matches_test.go`, `web_review_test.go`, `web_settings_test.go`, `web_race_test.go`, `prune_tokens_test.go`, `signatures_example_test.go`.
- `config.yaml` is gitignored and holds the operator's real secrets; `config.yaml.example` is committed with placeholders only and is guarded by `TestExampleConfigParsesAndHasNoSecrets` (`core/example_config_test.go`). Both keep identical signature and blacklist sets - change them together.
- Signature contents rules are gated by a mandatory-literal prefilter (`core/signatures.go`, `requiredLiterals`): a rule whose regex has no literal runs on every file, so keep a literal in new signatures.
- `blacklisted_paths` entries match by shape, not substring (`core/match.go`, `IsSkippableFile`): `build{sep}` is a component rule and no longer matches `rebuild/`.
- Every webhook is delivered through `core.WebhookQueue`; `webhook_queue.batch_size` coalesces Discord embeds into one message.
- AI review initialises only in `--web` / `--tui` (`initAIReview`, `cmd/shhgit/main.go`); plain terminal mode has none.
- Quality gate, all must be clean: `gofmt -l .`, `go vet ./...`, `staticcheck ./...`, `deadcode ./...`, `go test ./...`; CI pins `staticcheck@v0.8.1` and `deadcode@v0.50.0`.

## User preferences

- Code audits are read-only: report findings only; do not edit, create, or delete files unless explicitly asked.
- Finding format: numbered list; each item = Severity / Location (file:line) / What's wrong / Evidence (quoted code) / Suggested fix. Cite real code, do not speculate, do not pad, and state explicitly when a category has no findings.
- Remove dead, redundant and non-functional code; "nothing useless", "no fluff" - `deadcode` must stay clean.
- Only real, accurate content: no fabricated or guessed rules, provider prefixes or formats. Anything not provably correct gets removed, not kept.
- Keep the `config.yaml` signature structure unchanged (`part`/`match`/`regex`/`not_regex`/`name`/`priority`/`color`/`verifier`/`exclude_in_modes`) so signatures stay easy to hand-edit.
- Performance work must not make the machine unusable (the user runs scans on their own PC); prefer cutting total work and offer `--low-impact`.
- Verify claims, state plainly what was and was not actually tested, and prefer few well-reasoned suggestions over many shallow ones.
