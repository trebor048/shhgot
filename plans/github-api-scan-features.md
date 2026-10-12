# Plan: Port github-api-scan features into shhgit2

**Created**: 2026-09-29
**Branch**: main
**Status**: approved
**Approved**: 2026-09-29 (operator approved all 10 slices; implement all features then review)
**Gherkin persistence**: plan-file-only
<!-- No .feature files and no Godog/BDD manifest exist in this Go repo; the
     detection signal is `none`. Recorded as plan-file-only so no derived
     .feature artifacts are generated. Edit this line to change the decision. -->
**Scope enforcement**: none

## Decision stances (high-reversal-cost axes)

- **replace vs. merge**: **merge**. New settings are additive config sections;
  `config.yaml` is never rewritten wholesale. The only existing config write path
  (revoked-token pruning) is untouched.
- **auto-merge vs. direct-to-trunk**: **PR gated on green checks**. No direct merge.
- **scope**: **touch only the listed features and their required wiring**. Metrics,
  proxy and the region-specific push channels are deferred (see Deferred).
- **defaults**: every behaviour-changing feature is **off by default** (`storage`,
  `search`, `ai_filter`, `sources.gitlab`, validation cache disabled, `context_lines:
  0`), so an existing operator's scan output does not change until they opt in.
- **migrate vs. edit stub** / **format fidelity**: not applicable.

## Goal

Close the capability gaps between the sibling Python scanner `github-api-scan`
(`C:\Users\me\GIT\github-api-scan`) and shhgit2 by porting its genuinely valuable
features and discarding its dead code: historical discovery (GitHub code search), a
pluggable verifier registry with deep credential metadata and dynamic severity, a
persistent findings store with cross-run dedupe/resume/export, an opt-in AI
false-positive pre-filter, a shared resilient HTTP client, extra alert channels,
context/base-URL extraction, and a GitLab source. Each increment leaves the tree
buildable, tested and committable; the quality gate stays clean throughout.

**Provenance caveat for Slice 5.** In Python the ported providers live in
`validator_extended.py` (37 `EXTENDED_PLATFORMS` entries), which no entrypoint imports
— they are dormant there. They are ported here because shhgit2 *will* wire them. Each
is best-effort against a live endpoint until its `httptest` fixture is derived from
the documented response; any provider whose endpoint cannot be verified from evidence
is dropped, not guessed (AGENTS.md: "only real, accurate content"). The runtime
registry is seeded with endpoint shapes from two in-repo sources of evidence that
already exist: `core/token_validator.go` and the 44-entry provider table in
`cmd/apikey-check/main.go`.

## Acceptance Criteria

- [x] AC1 (Slice 1): webhook `Retry-After` and the per-host breaker are exercised with an injected clock; each webhook delivery performs exactly one transport attempt and is paced only by the queue (no client-side retry or pacing on that path).
- [x] AC2 (Slice 2): with `storage.enabled: true`, a finding recorded by a previous run is not re-alerted; the journal compacts past a threshold; the dedupe index is bounded by TTL + a max-entries cap; an unwritable location is reported as degraded and the scan continues; `--export-encrypted` + `--decrypt` round-trips records field-by-field and a wrong passphrase fails cleanly.
- [x] AC3 (Slice 3): `--search` publishes a match as source `github-search`; search requests never exceed 30/min; an off-allowlist raw URL produces zero download requests.
- [x] AC4 (Slice 4): a relay base URL is attached to a match, a private-address URL is not, and an Azure endpoint is detected.
- [x] AC5 (Slice 5): every existing executing network verifier (including `core/key_validator.go`) plus the ported ones is registry-backed; a registry test pins the exact expected count (see the tally in Step 5.2); the `core/verifiers` and `core/httpclient` import graphs contain no `core` dependency.
- [x] AC6 (Slice 6): a verified funded top-tier key classifies `critical`, a valid unfunded key `medium`, a dead key `low`, an explicit `unverified` outcome or absent outcome falls back to signature priority.
- [x] AC7 (Slice 7): a repeated key is not re-validated within the TTL; a dead host is skipped during cooldown; a protected AI host still receives requests.
- [x] AC8 (Slice 8): a placeholder is suppressed without any LLM call; on timeout, 500 and invalid JSON the finding is kept; suppressed findings are never written to the dedupe store.
- [x] AC9 (Slice 9): Slack receives a body carrying the finding and severity; Email reaches a fake SMTP sink; quiet hours suppress a medium but not a critical finding; a channel with `min_severity` high skips a medium finding.
- [x] AC10 (Slice 10): a public GitLab snippet is scanned and published as source `gitlab`; a 429 backs off.
- [ ] Non-functional (Pre-PR gate, no Gherkin): `config.yaml.example` is canonical and `config.yaml` mirrors it; `TestExampleConfigParsesAndHasNoSecrets` and `TestExampleConfigSignaturesAllCompile` green; quality gate clean.

## Slices

### Slice 1: Shared resilient outbound HTTP client

**Depends-on:** none
**Files:** `core/httpclient/client.go`, `core/httpclient/ssrf.go`, `core/httpclient/retry.go`, `core/httpclient/breaker.go`, `core/httpclient/client_test.go`, `core/config.go`, `core/token_validator.go`, `core/key_validator.go`, `core/validators.go`, `core/crypto_balance.go`, `core/webhook_queue.go`, `config.yaml`, `config.yaml.example`
**Invariants:** `go build ./...`, `go test ./core/...`
**Rollback point:** slice-start

**Layering rule:** `core/httpclient` is **core-free** — it defines its own `Config`
struct and does not import `core`; `core/config.go` maps the YAML `http:` section into
it. This keeps AC5's no-cycle build assertion true.

**Single-retrier rule:** the shared client owns retry/backoff for validators, AI,
GitHub and search. The webhook queue configures its client with `Retries: 0` **and
client-side pacing off**, keeping `sendWithRetry`/`reserveSendSlot` as the sole
retrier and pacer on that path. `TokenValidator.waitForRateLimit` is replaced by the
client's per-host pacing, so exactly one pacer exists per path.

**Behavior:**

```gherkin
Feature: Resilient outbound HTTP

  Scenario: Retry-After is honored on a 429
    Given a counting test server that answers 429 with Retry-After then 200
    When the shared client performs a request
    Then the recorded sleep equals the advertised value
    And the second attempt succeeds

  Scenario: A permanent status is not retried
    Given a counting test server that always answers 401
    When the shared client performs a request
    Then the server observed exactly one request

  Scenario: A webhook delivery is attempted once per queue retry
    Given the webhook queue configured with its own retries and client pacing disabled
    When a webhook responds 500
    Then the test server observed one request per queue attempt
    And no client-side pacing delay was recorded

  Scenario: A failing host trips the breaker but a protected host does not
    Given an injected clock and a host failing connection-class errors to the threshold
    When further calls are made before the cooldown elapses
    Then they fail fast without contacting the host
    And calls to a protected AI host still reach it while other hosts are open
```

**Steps:**

#### Step 1.1: HTTP client, config and SSRF guard

**Complexity**: standard
**IMPLEMENT**: `core/httpclient` (core-free) wrapping `*http.Client` with a pooled transport, its own `Config`, and `SafeURL(raw string) error`; `core/config.go` maps the `http:` YAML section.
**TEST**: `TestSafeURLRejectsPrivateAndPlainHTTP`; a single hydration test asserting every absent key in **every** new section (`http`, `storage`, `search`, `scanning.context_lines`, validation cache, `ai_filter`, `notify`, `sources.gitlab`) keeps its documented default.
**REFACTOR**: reserved-range table into `ssrf.go`.
**Files**: `core/httpclient/client.go`, `core/httpclient/ssrf.go`, `core/httpclient/client_test.go`, `core/config.go`, `config.yaml`, `config.yaml.example`
**Commit**: `feat(httpclient): pooled client + SSRF-safe URL guard`

#### Step 1.2: Retry, backoff and Retry-After (injected clock + sleep)

**Complexity**: standard
**IMPLEMENT**: Retry 408/429/500/502/503/504 with backoff + jitter; honour `Retry-After`/`X-RateLimit-Reset-After`; never retry 400/401/403/404; inject `now func() time.Time` **and** `sleep func(time.Duration)` so no test blocks.
**TEST**: status→retry table; Retry-After value assertion; one-request assertion on 401.
**REFACTOR**: export `httpclient.ParseRetryAfter`; delete the core duplicate.
**Files**: `core/httpclient/client.go`, `core/httpclient/retry.go`, `core/httpclient/client_test.go`, `core/webhook_queue.go`
**Commit**: `feat(httpclient): backoff, jitter and Retry-After handling`

#### Step 1.3: Per-host circuit breaker

**Complexity**: complex
**IMPLEMENT**: CLOSED/OPEN/HALF_OPEN; trips only on network-class failures; protected hosts and webhook hosts exempt; half-open probes use the injected clock.
**TEST**: state table with clock; protected host still receives requests while another is open; `-race`.
**REFACTOR**: one clock interface for retry and breaker.
**Files**: `core/httpclient/breaker.go`, `core/httpclient/client_test.go`
**Commit**: `feat(httpclient): per-host circuit breaker`

#### Step 1.4: Route existing callers through the shared client

**Complexity**: complex
**IMPLEMENT**: Replace ad-hoc `http.Client` construction in `token_validator.go`, `key_validator.go` (its eight clients used by `--scan-keys`), `validators.go`, `crypto_balance.go` and `webhook_queue.go`. Queue gets `Retries: 0` and client pacing disabled. Remove `TokenValidator.waitForRateLimit`.
**TEST**: `TestWebhookSingleAttemptPerQueueRetry`; validator pacing regression; existing validator/webhook/key-validator tests green.
**REFACTOR**: delete superseded timeout constants.
**Files**: `core/token_validator.go`, `core/key_validator.go`, `core/validators.go`, `core/crypto_balance.go`, `core/webhook_queue.go`
**Commit**: `refactor: funnel outbound HTTP through the shared client`

### Slice 2: Persistent findings store, dedupe, resume and export

**Depends-on:** none
**Files:** `core/store/store.go`, `core/store/journal.go`, `core/store/export.go`, `core/store/store_test.go`, `core/config.go`, `core/options.go`, `core/session.go`, `cmd/shhgit/main.go`, `cmd/shhgit/web.go`
**Rollback point:** slice-start

**Behavior:**

```gherkin
Feature: Persistent findings

  Scenario: A finding recorded by a previous run is not re-alerted
    Given storage enabled and a store written by a previous run, reopened
    When the same finding is observed again
    Then it is not delivered a second time

  Scenario: An interrupted append is ignored
    Given a store whose final write was interrupted
    When the store is opened
    Then every complete record loads and the partial one is ignored

  Scenario: The journal compacts and the index stays bounded
    Given a store past the record threshold and past the dedupe entry cap
    When the store is reopened
    Then the journal contains no duplicate records
    And entries older than the TTL are not retained

  Scenario: Delivery failure does not suppress a finding
    Given a finding whose delivery was not confirmed
    When the finding is observed again
    Then it is considered new

  Scenario: An unwritable location is reported
    Given storage enabled and a store writer that fails
    When the scanner starts
    Then it reports the store is degraded
    And the scan continues without dedupe

  Scenario: Encrypted export round-trips
    Given findings in the store
    When they are exported with --export-encrypted and read back with --decrypt
    Then each record's fields are identical
    And a wrong passphrase fails without emitting records
```

**Steps:**

#### Step 2.1: Store interface, journal and compaction

**Complexity**: complex
**IMPLEMENT**: `Store` interface; append-only journal with a rotated generation and compaction at `storage.compact_after`; load-time index rebuild tolerating a partial tail; **dedupe index bounded by `storage.dedupe_ttl_seconds` eviction at load plus `storage.max_dedupe_entries`** (LRU); files `0600`, dir `0700`; a failing writer sets a degraded flag (surfaced in terminal, TUI and dashboard) and the scan continues without dedupe.
**TEST**: append/reload round trip; partial-tail tolerance; compaction dedupes; TTL + max-entries eviction; degraded-on-write-failure (failing seam, not a real read-only FS, so it is deterministic on Windows and CI); `-race`.
**REFACTOR**: split bytes (`journal.go`) from index (`store.go`).
**Files**: `core/store/store.go`, `core/store/journal.go`, `core/store/store_test.go`, `core/config.go`, `core/options.go`
**Commit**: `feat(store): append-only journal with compaction and bounded index`

#### Step 2.2: Cross-run dedupe after confirmed delivery

**Complexity**: complex
**IMPLEMENT**: Fingerprint = `sha256(signature + source + match + repo + file)`. Define the **delivery-ack contract**: the webhook queue exposes an optional completion callback (fired on batch-send success, with failures leaving the fingerprint unwritten); broadcast-only runs (no webhook) count as confirmed. Record on confirmation only.
**TEST**: duplicate yields one delivery; unconfirmed delivery is treated as new; TTL expiry re-allows.
**REFACTOR**: one fingerprint function.
**Files**: `cmd/shhgit/main.go`, `core/store/store.go`, `core/store/store_test.go`, `core/webhook_queue.go`
**Commit**: `feat(store): dedupe confirmed deliveries with TTL`

#### Step 2.3: Resume cursors

**Complexity**: standard
**IMPLEMENT**: persist/reload events ETags/pages and search query/page cursors plus a bounded seen-blob set; `--no-resume` forces fresh.
**TEST**: round trip; resume skips consumed pages; blob bound.
**REFACTOR**: version cursor payloads.
**Files**: `core/store/store.go`, `core/session.go`, `cmd/shhgit/main.go`
**Commit**: `feat(store): resume scan cursors across runs`

#### Step 2.4: CSV and JSONL export

**Complexity**: standard
**IMPLEMENT**: `--export` + `--export-format csv|jsonl`, streaming; exports only fields this slice owns (id, timestamp, source, url, file, signature, matches, secret, line, stars, priority). Severity/provider columns are added in Slice 6, not here.
**TEST**: export/import field equality; CSV header/escaping.
**REFACTOR**: reuse `csvEscape`.
**Files**: `core/store/export.go`, `core/store/store_test.go`, `core/options.go`
**Commit**: `feat(store): CSV and JSONL findings export`

#### Step 2.5: Encrypted export

**Complexity**: complex
**IMPLEMENT**: `--export-encrypted <file>` and `--decrypt <file>`; AES-256-GCM + scrypt; passphrase read from an environment variable or interactive prompt, **never** a command-line argument; header stores salt+nonce; equality is field-by-field decoded.
**TEST**: round trip; tamper detection; wrong passphrase fails with no output.
**REFACTOR**: document the format.
**Files**: `core/store/export.go`, `core/store/store_test.go`, `core/options.go`
**Commit**: `feat(store): passphrase-encrypted findings export`

### Slice 3: GitHub code-search (dork) discovery

**Depends-on:** 1, 2
**Files:** `core/fetch.go`, `core/codesearch.go`, `core/codesearch_test.go`, `core/git.go`, `cmd/shhgit/search.go`, `cmd/shhgit/search_test.go`, `cmd/shhgit/main.go`, `cmd/shhgit/web.go`, `cmd/shhgit/tui.go`, `core/config.go`, `core/options.go`, `config.yaml`, `config.yaml.example`, `README.md`
**Rollback point:** slice-start

**Behavior:**

```gherkin
Feature: GitHub code search

  Scenario: A dork finds a leaked key
    Given a fake search API returning one file containing an OpenAI key
    When the search scanner runs
    Then one match is published with source github-search

  Scenario: The search rate limit is respected
    Given a counting search API and injected clock and sleep seams
    When the scanner runs with many queries
    Then the recorded request count over any simulated minute never exceeds thirty

  Scenario: A hostile download URL is refused
    Given a counting server and a search result whose raw URL points at an unlisted host
    When the scanner fetches it
    Then the download server observed zero requests
```

**Steps:**

#### Step 3.1: Search client, dorks and host allowlist

**Complexity**: complex
**IMPLEMENT**: `core/codesearch.go` with paginated `GET /search/code`; dorks from **high-signal prefixes/anchors** (not generic literals), ranked and capped, operator override file; raw-URL allowlist; injectable base URL.
**TEST**: dork derivation excludes generic literals; allowlist rejection; base-URL injection.
**REFACTOR**: ranking table.
**Files**: `core/codesearch.go`, `core/codesearch_test.go`
**Commit**: `feat(search): code-search client and high-signal dork builder`

#### Step 3.2: Rate limiting and pagination

**Complexity**: standard
**IMPLEMENT**: per-page sleep via the injected `sleep`, reset parking, 1000-result cap, `incomplete_results`, token rotation.
**TEST**: pagination; 30/min bound via counting server + clock/sleep; cap.
**REFACTOR**: reuse `ParseRetryAfter`.
**Files**: `core/codesearch.go`, `core/codesearch_test.go`
**Commit**: `feat(search): pagination and search rate limiting`

#### Step 3.3: Core fetch helper and cmd-level scan pipeline

**Complexity**: complex
**IMPLEMENT**: Add `core/fetch.go` (core-free) exposing a bounded download helper (size/binary/extension gates + host allowlist), used by Slices 3 and 10. Add `GITHUB_SEARCH_SOURCE` + `sourceName` case in `core/git.go`/`cmd/shhgit/main.go`. Add cmd-level `scanContents(content, displayPath, source, publisher)` with an injected publisher (core keeps only pure `Signature` matching).
**TEST**: end-to-end fake server publishes exactly one match; oversized/binary skipped; publisher injected.
**REFACTOR**: remove the duplicated inline loop from `checkSignatures`.
**Files**: `core/fetch.go`, `cmd/shhgit/search.go`, `cmd/shhgit/search_test.go`, `core/git.go`, `cmd/shhgit/main.go`, `cmd/shhgit/web.go`
**Commit**: `feat(search): core fetch helper and cmd-level scan pipeline`

#### Step 3.4: Config, flags and docs

**Complexity**: trivial
**IMPLEMENT**: `search:` section (off by default); `--search`/`--search-max-results`/`--search-sleep-ms`; a missing-token/code-search-scope warning surfaced in terminal, TUI (`cmd/shhgit/tui.go`) and dashboard (`cmd/shhgit/web.go`); README + both config files.
**TEST**: config parse; example-config compile; warning path per surface.
**REFACTOR**: align key naming.
**Files**: `core/config.go`, `core/options.go`, `cmd/shhgit/tui.go`, `cmd/shhgit/web.go`, `config.yaml`, `config.yaml.example`, `README.md`
**Commit**: `feat(search): configuration, flags and documentation`

### Slice 4: Context window and base-URL extraction

**Depends-on:** 1
**Files:** `core/context.go`, `core/context_test.go`, `cmd/shhgit/web.go`, `cmd/shhgit/main.go`, `core/match.go`, `core/config.go`, `cmd/shhgit/web_review.go`

**Behavior:**

```gherkin
Feature: Finding context and endpoints

  Scenario: A relay base URL is extracted and ranked
    Given a file containing a key and several URLs
    When the match is published
    Then the ranked known-relay URL is attached

  Scenario: A private endpoint is rejected
    Given a file containing a key and a URL at a private address
    When the match is published
    Then no base URL is attached

  Scenario: An Azure endpoint is detected
    Given a file containing an Azure OpenAI key and its endpoint host
    When the match is published
    Then the Azure endpoint is attached

  Scenario: Context is bounded
    Given context_lines is 10
    When a match on a deep line is captured
    Then the context contains at most 21 lines
```

**Steps:**

#### Step 4.1: Bounded context extraction

**Complexity**: standard
**IMPLEMENT**: `line±context_lines` from cached contents, capped to `maxStoreFileBytes`; `scanning.context_lines` **default 0** (off; opt-in).
**TEST**: window bounds; disabled default; multi-byte safety.
**REFACTOR**: reuse the line-index cache.
**Files**: `core/context.go`, `core/context_test.go`, `core/match.go`, `core/config.go`
**Commit**: `feat(context): opt-in bounded context window`

#### Step 4.2: URL extraction, ranking and SSRF-safe selection

**Complexity**: complex
**IMPLEMENT**: extract/normalise/rank candidates, validate via `httpclient.SafeURL`, Azure detection.
**TEST**: ranking; private/hostile rejected; Azure detected.
**REFACTOR**: table-drive relay keywords.
**Files**: `core/context.go`, `core/context_test.go`
**Commit**: `feat(context): extract and rank relay/Azure endpoints`

#### Step 4.3: Attach to Match and review prompt

**Complexity**: standard
**IMPLEMENT**: `BaseURL`/`Context` on `Match`/`TokenResult`; include in the AI review prompt.
**TEST**: API exposes fields; prompt contains context.
**REFACTOR**: one prompt helper.
**Files**: `cmd/shhgit/web.go`, `cmd/shhgit/main.go`, `cmd/shhgit/web_review.go`
**Commit**: `feat(context): surface base URL and context in API and AI review`

### Slice 5: Verifier registry and deep metadata

**Depends-on:** 1, 4
**Files:** `core/verifiers/registry.go`, `core/verifiers/providers.go`, `core/verifiers/pairing.go`, `core/verifiers/registry_test.go`, `core/verifiers/*_test.go`, `core/token_validator.go`, `core/key_validator.go`, `core/validators.go`, `core/signatures.go`, `core/crypto_balance_test.go`, `core/token_validator_test.go`, `core/validators_test.go`, `core/discord_validator_test.go`, `cmd/shhgit/web.go`, `cmd/shhgit/main.go`, `cmd/shhgit/tui.go`, `cmd/shhgit/tui_test.go`
**Rollback point:** slice-start

**Registry scope (decided):** `core/verifiers` is the single source of truth and
**absorbs** the existing verifiers in `token_validator.go` (prefix detection + the AI
providers), the eight validators behind `core/key_validator.go` (`--scan-keys`), and
the standalone `ValidateDiscordToken`/`ValidateTelegramToken`/`ValidateSSHKey`/
`ValidateCryptoKey` in `core/validators.go`. It is **core-free** (no `core` import).
Seam exports: `verifiers.Lookup(name)`, `verifiers.Detect(keyOrPrefix)`, `verifiers.All()`.
`token_validator.go`'s `DetectProvider` and `signatures.go`'s `knownVerifier` delegate
to it. The 44-entry table in `cmd/apikey-check/main.go` is **evidence** for endpoint
shapes and is intentionally left as a standalone utility (not required to consume the
registry); its entries seed the ported providers.

**Outcome enum:** `valid`, `invalid`, `quota_limited`, `unverified`, `dead`.

**Behavior:**

```gherkin
Feature: Provider verification registry

  Scenario: Every registered verifier is runnable
    Given the registry is initialised
    Then the entry count equals the pinned expected tally
    And each entry has a name, a category and a base URL

  Scenario: A valid key yields identity
    Given a counting test server answering a provider's success body
    When the key is verified
    Then the outcome is valid and carries the decoded identity

  Scenario: A multi-credential provider needs its companion value
    Given an auth token with no companion value in context
    When it is verified
    Then the outcome is unverified and the server observed zero requests

  Scenario: Quota is distinguished from invalid
    Given a provider that answers 429 once and 401 for another key
    When each key is verified
    Then the 429 key is quota-limited and the 401 key is invalid
```

**Steps:**

#### Step 5.1: Registry interface, seams and first migration

**Complexity**: complex
**IMPLEMENT**: `Verifier{Name, Category, KeyPrefixes, Needs, Build(c Credential, base string) (*http.Request, error), Test(resp, body) Outcome}`; registry keyed by config `verifier:`; migrate the GitHub verifier and one AI verifier first; define which `token_validator.go`/`key_validator.go`/`validators.go` arms are absorbed.
**TEST**: `Lookup`/`Detect`/`All`; migration parity; a build assertion that `core/verifiers` does not import `core`.
**REFACTOR**: delete absorbed arms as each migrates.
**Files**: `core/verifiers/registry.go`, `core/verifiers/providers.go`, `core/verifiers/registry_test.go`, `core/signatures.go`
**Commit**: `feat(verifiers): registry seams replacing the verifier switches`

#### Step 5.2: Migrate the remaining existing verifiers

**Complexity**: complex
**IMPLEMENT**: Move every **executing** existing verifier (AI providers, GitHub, Discord, Slack, Stripe, NPM, Telegram, Square, SendGrid, DigitalOcean, plus the eight `key_validator.go` providers and `crypto_balance`/`ssh`) into the registry. Verifiers that are accepted by config but have no execution path today (Mistral, Cohere as standalone constants; Stability/AssemblyAI/Deepgram) are either given a real runnable validator (Mistral/Cohere via the OpenAI-compatible path) or documented as shape-only `unverified`.
**TEST**: registry count equals the **pinned tally** (enumerated in the test, not a guess); fixture coverage asserted by iterating `registry.All() ∩ fixtureTable`; `DetectProvider` parity.
**REFACTOR**: remove empty switches.
**Files**: `core/verifiers/providers.go`, `core/verifiers/registry_test.go`, `core/token_validator.go`, `core/key_validator.go`, `core/validators.go`, `core/signatures.go`, `core/crypto_balance_test.go`, `core/token_validator_test.go`, `core/validators_test.go`, `core/discord_validator_test.go`
**Commit**: `feat(verifiers): migrate all executing verifiers into the registry`

#### Step 5.3: VCS/CI providers

**Complexity**: standard
**IMPLEMENT**: GitLab, Bitbucket, CircleCI, Travis, Docker Hub.
**TEST**: one fixture each (injected base URL).
**REFACTOR**: shared decode helper.
**Files**: `core/verifiers/vcs.go`, `core/verifiers/vcs_test.go`
**Commit**: `feat(verifiers): GitLab, Bitbucket, CircleCI, Travis, Docker Hub`

#### Step 5.4: Cloud and monitoring providers

**Complexity**: standard
**IMPLEMENT**: Cloudflare (token + global-key fallback), Vercel, Heroku; Datadog, Sentry, New Relic (GraphQL), PagerDuty.
**TEST**: one fixture each; Cloudflare fallback; GraphQL guard.
**REFACTOR**: dedupe Bearer builders.
**Files**: `core/verifiers/cloud.go`, `core/verifiers/monitoring.go`, `*_test.go`
**Commit**: `feat(verifiers): Cloudflare, Vercel, Heroku, Datadog, Sentry, New Relic, PagerDuty`

#### Step 5.5: Collab, analytics, CRM, maps, payments and messaging providers

**Complexity**: standard
**IMPLEMENT**: Notion, Linear, Asana, Airtable, Atlassian/Jira; Segment, Mixpanel, Amplitude, Algolia; Zendesk, Intercom, Freshdesk; Mapbox, Google Maps; PayPal; Mailgun, Mailchimp, Twilio.
**TEST**: one fixture each including Twilio (Basic SID+token).
**REFACTOR**: group by category.
**Files**: `core/verifiers/{collab,analytics,crm,maps,payments,messaging}.go`, `*_test.go`
**Commit**: `feat(verifiers): collab, analytics, CRM, maps, payments, messaging providers`

#### Step 5.6: Context credential pairing

**Complexity**: complex
**IMPLEMENT**: For verifiers with `Needs`, scan the ±context window (Slice 4) for companion values; run only when all are present, else `unverified` with no request.
**TEST**: Twilio SID+token verifies; missing SID unverified with zero requests.
**REFACTOR**: table-drive companion regexes.
**Files**: `core/verifiers/pairing.go`, `core/verifiers/pairing_test.go`
**Commit**: `feat(verifiers): context credential pairing`

#### Step 5.7: Metadata and richer results (crypto_balance kept)

**Complexity**: complex
**IMPLEMENT**: Extract identity/tier/balance/RPM/high-value/latency; extend `TokenResult` and `OnTokenResult`; update all consumers (`pushTokenResult`, `AddTUIToken`, `tui_test.go`). `crypto_balance` **keeps** a zero-balance key with `outcome=unverified`, never drops it.
**TEST**: metadata per provider; zero-balance key returned with `unverified`; TUI/web callbacks compile and pass.
**REFACTOR**: remove superseded detection arms.
**Files**: `core/verifiers/providers.go`, `core/token_validator.go`, `core/signatures.go`, `cmd/shhgit/web.go`, `cmd/shhgit/main.go`, `cmd/shhgit/tui.go`, `cmd/shhgit/tui_test.go`
**Commit**: `feat(verifiers): metadata, RPM, balance and richer token results`

### Slice 6: Dynamic finding severity

**Depends-on:** 5
**Files:** `core/severity.go`, `core/severity_test.go`, `cmd/shhgit/web.go`, `cmd/shhgit/main.go`, `core/webhook_queue.go`, `cmd/shhgit/web/src/components/MatchTable.jsx`, `cmd/shhgit/web/src/components/TokensPanel.jsx`

**Behavior:**

```gherkin
Feature: Finding severity

  Scenario: A funded top-tier key is critical
    Given a verified key with tier GPT-4 and balance 150
    When severity is classified
    Then the severity is critical

  Scenario: A valid but unfunded key is medium
    Given a verified key with no tier and zero balance
    When severity is classified
    Then the severity is medium

  Scenario: A dead key is low
    Given a key verified invalid
    When severity is classified
    Then the severity is low

  Scenario: An unverified outcome falls back to signature priority
    Given a key with an explicit unverified outcome
    When severity is classified
    Then the severity equals the signature priority mapping

  Scenario: The API returns priority and severity as distinct fields
    Given findings with differing rule priority and verified severity
    When /api/matches is read
    Then each item carries both fields

  Scenario: Filtering by severity
    Given cached findings of mixed severity
    When the dashboard requests severity=critical
    Then only critical findings are returned
```

**Steps:**

#### Step 6.1: Classifier

**Complexity**: standard
**IMPLEMENT**: `Severity` + `Classify(sigPriority, *verifiers.Outcome)`; `valid`+top-tier+≥100/unlimited ⇒ critical; `valid`+high-tier/≥10/high-value ⇒ high; `valid` ⇒ medium; `invalid`/`dead` ⇒ low; **explicit `unverified`** (including crypto zero-balance) and no outcome ⇒ signature-priority mapping.
**TEST**: full truth table including explicit-unverified and dead=low.
**REFACTOR**: one threshold block.
**Files**: `core/severity.go`, `core/severity_test.go`
**Commit**: `feat(severity): classify findings from verifier metadata`

#### Step 6.2: Surface severity without clashing with priority

**Complexity**: standard
**IMPLEMENT**: `Severity`/`Verified` on `Match`/`TokenResult`; `?severity=` on `/api/matches`; webhook colour by severity; UI labels `priority` as "rule priority" and `severity` as "verified severity" using native controls; add severity/provider/tier/balance columns to export here.
**TEST**: API carries distinct fields + filter; webhook colour map; Go embed test for `web/dist`.
**REFACTOR**: centralise severity→colour.
**Files**: `cmd/shhgit/web.go`, `cmd/shhgit/main.go`, `core/webhook_queue.go`, `core/store/export.go`, `cmd/shhgit/web/src/components/MatchTable.jsx`, `cmd/shhgit/web/src/components/TokensPanel.jsx`
**Commit**: `feat(severity): expose verified severity distinctly from rule priority`

### Slice 7: Validation cache and domain health (opt-in)

**Depends-on:** 5
**Files:** `core/validation_cache.go`, `core/validation_cache_test.go`, `core/token_validator.go`, `core/config.go`, `config.yaml`, `config.yaml.example`

**Behavior:**

```gherkin
Feature: Validation caching

  Scenario: A repeated key is not re-validated within the TTL
    Given caching enabled, a counting provider and an injected clock
    When the same key is verified twice before the TTL
    Then the provider observed one request

  Scenario: A dead host is skipped during cooldown
    Given caching enabled and a host that failed to the dead threshold
    When a key for that host is verified before cooldown ends
    Then it is answered connection-error without a request

  Scenario: A protected AI host still receives requests
    Given another host is dead
    When a key for a protected AI host is verified
    Then the protected host observed the request
```

**Steps:**

#### Step 7.1: Sharded TTL/LRU validation cache

**Complexity**: standard
**IMPLEMENT**: `key+base_url` → outcome with TTL/max entries, 16 shards, LRU; **`performance.validators.cache_enabled` default false**; injected clock.
**TEST**: TTL with clock; eviction; `-race`.
**REFACTOR**: shard helper.
**Files**: `core/validation_cache.go`, `core/validation_cache_test.go`, `core/config.go`
**Commit**: `feat(cache): opt-in TTL/LRU validation result cache`

#### Step 7.2: Domain health gating

**Complexity**: standard
**IMPLEMENT**: DEGRADED/UNHEALTHY/DEAD with recovery, DEAD short-circuit for a cooldown, protected hosts exempt; injected clock.
**TEST**: state table; protected host still requested while another is dead; recovery.
**REFACTOR**: constants block.
**Files**: `core/validation_cache.go`, `core/validation_cache_test.go`, `core/token_validator.go`, `core/config.go`
**Commit**: `feat(cache): skip known-dead hosts`

### Slice 8: AI false-positive pre-filter (opt-in)

**Depends-on:** 2, 3, 4, 5
**Files:** `core/aifilter/quickfilter.go`, `core/aifilter/llm.go`, `core/aifilter/*_test.go`, `cmd/shhgit/aifilter.go`, `cmd/shhgit/main.go`, `cmd/shhgit/web.go`, `core/config.go`, `config.yaml`, `config.yaml.example`

**Suppression safety:** suppressed findings are **never** written to the dedupe store;
the classifier fails **open** on any error.

**Behavior:**

```gherkin
Feature: AI false-positive filtering

  Scenario: A placeholder is filtered without an LLM call
    Given a counting LLM backend and a placeholder key in a test path
    When the filter runs
    Then the finding is suppressed
    And the backend observed zero requests

  Scenario: A failing backend keeps the finding
    Given a backend that answers timeout, then 500, then invalid JSON
    When candidates are filtered
    Then every finding is kept

  Scenario: A confident placeholder classification suppresses
    Given a backend returning placeholder/high confidence
    When the candidate is filtered
    Then it is suppressed and appears in the suppressed listing

  Scenario: A suppressed finding is not remembered
    Given a suppressed finding
    When the scanner restarts and the same finding recurs
    Then it is evaluated again

  Scenario: Repeated filter failures are visible
    Given the backend has failed repeatedly
    When the filter status endpoint is read
    Then it reports degraded
```

**Steps:**

#### Step 8.1: Deterministic quick filter

**Complexity**: standard
**IMPLEMENT**: ordered rules; advisory metadata retained.
**TEST**: truth table.
**REFACTOR**: one placeholder union.
**Files**: `core/aifilter/quickfilter.go`, `core/aifilter/quickfilter_test.go`
**Commit**: `feat(aifilter): deterministic false-positive quick filter`

#### Step 8.2: LLM classifier (fail-open)

**Complexity**: complex
**IMPLEMENT**: reuse `aiproviders` (Ollama default); strict-JSON prompt; suppress only on TEST/EXAMPLE/PLACEHOLDER/FAKE with MEDIUM/HIGH confidence; error/timeout/500/invalid-JSON ⇒ keep; bounded LRU cache.
**TEST**: mock suppress/keep; timeout, 500 and invalid-JSON each keep; cache.
**REFACTOR**: conservative parse default.
**Files**: `core/aifilter/llm.go`, `core/aifilter/llm_test.go`
**Commit**: `feat(aifilter): opt-in LLM classifier that fails open`

#### Step 8.3: Publish-path wiring, suppressed view and degraded status

**Complexity**: standard
**IMPLEMENT**: bounded pool (default 2); consult in `publish()`; suppressed ring only (never the store); `/api/suppressed` + dashboard tab with empty/overflow states; a degraded flag exposed via an API endpoint when failures persist.
**TEST**: suppressed absent from feed and store but present in the API; degraded flag after N failures.
**REFACTOR**: shared ring helper.
**Files**: `cmd/shhgit/aifilter.go`, `cmd/shhgit/main.go`, `cmd/shhgit/web.go`, `cmd/shhgit/web/src/components/*`
**Commit**: `feat(aifilter): filter findings visibly and safely`

### Slice 9: Alert channels and notification policy

**Depends-on:** 2, 6
**Files:** `core/notify/channel.go`, `core/notify/{discord,slack,telegram,email,file,sound}.go`, `core/notify/*_test.go`, `core/webhook_queue.go`, `core/config.go`, `cmd/shhgit/main.go`, `config.yaml`, `config.yaml.example`

**Scope:** Discord, Telegram, generic webhook (existing) plus Slack, Email, File,
Sound. Region push services are deferred.

**Behavior:**

```gherkin
Feature: Multi-channel alerts

  Scenario: Slack receives the finding and severity
    Given a counting Slack webhook and a critical finding
    When it is notified
    Then the body carries the finding and its severity

  Scenario: Email reaches an SMTP sink
    Given a fake SMTP server and an Email channel
    When a finding is notified
    Then the sink received one message

  Scenario: A misconfigured channel is reported at startup
    Given a channel whose endpoint rejects a test delivery
    When the scanner starts with test delivery enabled
    Then a warning names that channel

  Scenario: Quiet hours suppress medium but not critical
    Given quiet hours active and an injected clock
    When a medium and a critical finding are notified
    Then only the critical one is delivered

  Scenario: Per-channel minimum severity
    Given a channel with min_severity high
    When a medium finding is notified
    Then the channel is skipped
```

**Steps:**

#### Step 9.1: Channel interface and migration

**Complexity**: complex
**IMPLEMENT**: `Channel{Name, MinSeverity, Build, Send}`; Discord/Telegram/generic on it; queue stays transport; no double-retry.
**TEST**: existing tests green; Discord snapshot.
**REFACTOR**: delete the URL branch in `SendMatchWebhook`.
**Files**: `core/notify/channel.go`, `core/notify/discord.go`, `core/notify/telegram.go`, `core/notify/generic.go`, `core/webhook_queue.go`, `core/validators.go`
**Commit**: `refactor(notify): channel interface over the existing queue`

#### Step 9.2: Slack, Email, File, Sound + opt-in test delivery

**Complexity**: standard
**IMPLEMENT**: Slack, Email (dialer seam for a fake SMTP sink), File, Sound; a startup test delivery **behind `notify.test_on_start` (default false)**, results routed through an injected logger (not raw stdout) and deduped across restarts.
**TEST**: Slack body fixture; fake SMTP sink receives one message; misconfigured channel warns via the injected logger; test delivery off by default.
**REFACTOR**: shared masked-key + severity rendering.
**Files**: `core/notify/slack.go`, `core/notify/email.go`, `core/notify/file.go`, `core/notify/sound.go`, `core/notify/*_test.go`, `cmd/shhgit/main.go`
**Commit**: `feat(notify): Slack, Email, File, Sound with opt-in validation`

#### Step 9.3: Policy: min severity, quiet hours, digest

**Complexity**: complex
**IMPLEMENT**: per-channel `min_severity`; quiet hours with CRITICAL bypass; daily digest from the store (no-op with a logged note when storage is disabled/degraded); injected clock.
**TEST**: cross-midnight with clock; critical bypass; digest formatting; digest no-op without storage.
**REFACTOR**: one policy evaluator.
**Files**: `core/notify/policy.go`, `core/notify/policy_test.go`, `cmd/shhgit/main.go`, `core/config.go`
**Commit**: `feat(notify): severity routing, quiet hours and daily digest`

### Slice 10: GitLab public source

**Depends-on:** 3
**Files:** `core/gitlab.go`, `core/gitlab_test.go`, `cmd/shhgit/main.go`, `core/config.go`, `config.yaml`, `config.yaml.example`

**Behavior:**

```gherkin
Feature: GitLab snippets

  Scenario: A public snippet is scanned
    Given a fake GitLab API returning one snippet containing a key
    When the source runs
    Then a match is published with source gitlab

  Scenario: A rate-limit answer backs off
    Given a counting GitLab API answering 429 with Retry-After and injected sleep
    When the source polls
    Then it waits before retrying
```

**Steps:**

#### Step 10.1: GitLab client

**Complexity**: standard
**IMPLEMENT**: `GET /api/v4/snippets/public` (no auth), pagination and raw download via `core/fetch.go` (Slice 3); injectable base URL; backoff via the shared sleep seam.
**TEST**: fake API pagination + download; 429 backoff with no real sleep.
**REFACTOR**: reuse the core fetch helper.
**Files**: `core/gitlab.go`, `core/gitlab_test.go`
**Commit**: `feat(gitlab): public snippet source`

#### Step 10.2: Config and enablement

**Complexity**: standard
**IMPLEMENT**: `sources.gitlab` (off by default) and start the source; `sourceName` already maps `GITLAB_SOURCE`, so only enablement/config wiring remains.
**TEST**: enabled publishes; disabled makes zero requests.
**REFACTOR**: align with the search config shape.
**Files**: `cmd/shhgit/main.go`, `core/config.go`, `config.yaml`, `config.yaml.example`
**Commit**: `feat(gitlab): wire public snippet source behind config`

## Complexity Classification

| Rating | Steps |
|--------|-------|
| `trivial` | 3.4 |
| `standard` | 1.1, 1.2, 2.3, 2.4, 3.2, 4.1, 4.3, 5.3–5.5, 6.1, 6.2, 7.1, 7.2, 8.1, 8.3, 9.2, 10.1, 10.2 |
| `complex` | 1.3, 1.4, 2.1, 2.2, 2.5, 3.1, 3.3, 4.2, 5.1, 5.2, 5.6, 5.7, 8.2, 9.1, 9.3 |

## Pre-PR Quality Gate

- [x] All tests pass (`go test ./...`, plus `-race` on touched packages) — 715 passed
- [x] Compile check (`go build ./...`)
- [x] Linters pass (`gofmt -l .`, `go vet ./...`, `staticcheck ./...`, `deadcode ./...`)
- [ ] `/code-review` passes — pending the operator's review passes
- [x] `config.yaml.example` is canonical; `config.yaml` mirrors it; README updated
- [x] `npm run build` in `cmd/shhgit/web` if `web/src` changed (manual gate, not a test)

## Deferred (out of scope for this plan)

| Feature | Reason for deferral |
|---|---|
| Metrics endpoint + latency histogram + dashboard cards (compared F10) | Purely observability; no acceptance criterion depends on it. |
| Proxy rotation / SOCKS5 (compared F11) | Upstream pool is unwired and would promote `golang.org/x/net` from indirect to direct; no stated need on a single-operator PC. |
| Feishu, DingTalk, Bark, ServerChan, PushPlus channels | Region-specific push services; disproportionate surface. Add on request. |

## Skipped (low value)

| Finding | Rationale (one line) |
|---|---|
| `regex_extended.py` (106 patterns) | Never imported by any Python scanner path; shhgit2 already ships ~198 broader signatures. |
| `scanner_async.py` | Un-importable: references `scan_github_code`/`extract_keys_from_content` that do not exist. |
| `validator_optimized.py`, `cache_manager.py`, `connection_pool.py`, `retry_handler.py`, `batch_validator.py`, `proxy_pool.py` (as code) | Dormant in practice (imported by `validator.py`/`main_v2.1`/`main_v2.2` but never instantiated on a scan path); only the concepts are ported (Slices 1, 7). |
| `source_pastebin.py` | Needs a paid Scraping API key; its free path is unreachable. |
| `source_searchcode.py` | Unreliable API; context is taken from the wrong offset (`content[:500]`). |
| `source_gist.py` / `source_realtime.py` as new sources | shhgit2 already covers Gists and the events feed; the Compare-API push-range scan is a possible future enhancement. |
| `ExtendedKeyStatus.PARTIAL` / `PriorityDynamicQueue` / claimed Prometheus export | Declared but never emitted/used/implemented upstream. |
| Python memory-pressure dynamic queue sizing | Machine-hostile; conflicts with the `--low-impact` preference. |
| The four `main_*.py` version files | Process artifact, not a feature. |
| Python AI fallback code | Inconsistent contract: `_parse_response` defaults `is_likely_real=False` when the field is missing while the no-response/exception paths return `True`. Slice 8 defines one fail-open contract. |

## Risks & Open Questions

- **No spec artifacts** (`docs/specs/**`, `specs/**` absent). Planning used the comparative analysis only; if acceptance semantics exist elsewhere, this plan may diverge. Owner: operator.
- **Multi-milestone program.** Land per wave behind off-by-default config: Wave A = Slice 1; Wave B = Slice 2; Wave C = Slice 3; Wave D = Slice 4 **then** 5 (both edit `TokenResult`/`web.go`/`main.go`; order 4 first); Wave E = Slices 6+7; Wave F = Slices 8+10; Wave G = Slice 9.
- **Resolved — store engine**: JSONL journal (zero-dependency, no database).
- **Resolved — export crypto**: stdlib AES-GCM + scrypt.
- **Resolved — verifier scope**: registry absorbs every executing verifier (including `key_validator.go`); `cmd/apikey-check` remains a standalone evidence source and is not required to consume the registry.
- **`crypto_balance` semantics**: a zero-balance key is **kept** as `unverified` rather than dropped; call out in release notes.
- **At-rest exposure**: the journal stores raw secret matches in plaintext (0600/0700); documented.
- **GitHub code-search scope**: tokens without code-search access yield zero results; the warning surfaces in all three UIs.
- **Canonical config**: `config.yaml.example` is canonical; `config.yaml` mirrors it (the example currently has one extra `JSON Web Token` signature — reconcile separately).
- **Merge serialisation**: Slices 3, 6, 8, 10 edit `publish()`; land waves sequentially as ordered.

## Plan Review Summary

**Plan tier: complex — reviewers: Acceptance, Design, Strategic, UX (4 dispatched; the
`plan-review-*` persona subagents failed with a harness model error, so the same lenses
were re-run via the general subagent).**

Two review iterations were run; the maximum of 2 was reached and reviewers still return
`needs-revision` on residual (mostly documentation-level) points, so this is escalated
to the operator rather than looped further.

Findings folded into the plan between iterations:
- Acceptance: verifier-count traceability, injectable provider base URL, dead-key severity row, `--export-encrypted` named + equality defined, sleep/clock seams, observable rate limits, JSONL-internal wording removed, fake SMTP sink, per-step clocks.
- Design: webhook double-retry eliminated (queue is sole retrier + pacer), store compaction + bounded/TTL index, `core/fetch.go` seam, core-free `httpclient`/`verifiers`, `OnTokenResult` consumers added, Slice 1.4 rated complex, Slice 10 depends on 3.
- Strategic: provenance caveat, verifier count reconciled and pinned, registry scope stated, metrics/proxy/region channels deferred, wave order adopted, dedupe-after-confirmed-delivery, suppressed-never-stored, skipped rationales corrected.
- UX: unwritable-store degraded state, all new features off by default, export columns deferred to Slice 6, severity vs priority labelled distinctly, opt-in startup test delivery, passphrase via env/prompt.

Residual reviewer concerns (not blockers to starting, to confirm at approval):
- Acceptance: exact registry tally must be pinned in the test (currently "pinned expected tally"); some ACs (Azure, Slack shape) map to step tests rather than a dedicated scenario.
- Design: webhook-queue completion-ack contract needs a concrete type before Step 2.2 can be tested (specified in prose, not as a Go signature).
- Strategic: whether `cmd/apikey-check` should be refactored to consume the registry (currently left standalone).
- UX: TUI/dashboard degraded-state copy for the store and AI filter is specified but not designed pixel-level.

## Build Progress

### Slices

- [x] Slice 1: Shared resilient outbound HTTP client
  - [x] Step 1.1: HTTP client, config and SSRF guard
  - [x] Step 1.2: Retry, backoff and Retry-After (injected clock + sleep)
  - [x] Step 1.3: Per-host circuit breaker
  - [x] Step 1.4: Route existing callers through the shared client
- [x] Slice 2: Persistent findings store, dedupe, resume and export
  - [x] Step 2.1: Store interface, journal and compaction
  - [x] Step 2.2: Cross-run dedupe after confirmed delivery
  - [x] Step 2.3: Resume cursors
  - [x] Step 2.4: CSV and JSONL export
  - [x] Step 2.5: Encrypted export
- [x] Slice 3: GitHub code-search (dork) discovery
  - [x] Step 3.1: Search client, dorks and host allowlist
  - [x] Step 3.2: Rate limiting and pagination
  - [x] Step 3.3: Core fetch helper and cmd-level scan pipeline
  - [x] Step 3.4: Config, flags and docs
- [x] Slice 4: Context window and base-URL extraction
  - [x] Step 4.1: Bounded context extraction
  - [x] Step 4.2: URL extraction, ranking and SSRF-safe selection
  - [x] Step 4.3: Attach to Match and review prompt
- [x] Slice 5: Verifier registry and deep metadata
  - [x] Step 5.1: Registry interface, seams and first migration
  - [x] Step 5.2: Migrate the remaining existing verifiers
  - [x] Step 5.3: VCS/CI providers
  - [x] Step 5.4: Cloud and monitoring providers
  - [x] Step 5.5: Collab, analytics, CRM, maps, payments and messaging providers
  - [x] Step 5.6: Context credential pairing
  - [x] Step 5.7: Metadata and richer results (crypto_balance kept)
- [x] Slice 6: Dynamic finding severity
  - [x] Step 6.1: Classifier
  - [x] Step 6.2: Surface severity without clashing with priority
- [x] Slice 7: Validation cache and domain health (opt-in)
  - [x] Step 7.1: Sharded TTL/LRU validation cache
  - [x] Step 7.2: Domain health gating
- [x] Slice 8: AI false-positive pre-filter (opt-in)
  - [x] Step 8.1: Deterministic quick filter
  - [x] Step 8.2: LLM classifier (fail-open)
  - [x] Step 8.3: Publish-path wiring, suppressed view and degraded status
- [x] Slice 9: Alert channels and notification policy
  - [x] Step 9.1: Channel interface and migration
  - [x] Step 9.2: Slack, Email, File, Sound + opt-in test delivery
  - [x] Step 9.3: Policy: min severity, quiet hours, digest
- [x] Slice 10: GitLab public source
  - [x] Step 10.1: GitLab client
  - [x] Step 10.2: Config and enablement

## Build Outcome

Implemented 2026-09-29. Final gate: `gofmt -l .` clean, `go vet ./...` clean, `staticcheck ./...` clean, `deadcode ./...` clean, `go test ./...` → 715 passed (10 packages).

Delivered: `core/httpclient` (pooled client, retry/Retry-After, per-host breaker, SSRF guard); `core/store` (JSONL journal, compaction, bounded dedupe, cursors, CSV/JSONL/encrypted export); GitHub code-search source; `core/context.go` (context window + relay/Azure base-URL extraction); `core/verifiers` (56 providers, registry, pairing); `core/severity.go` + dashboard/API/webhook/TUI severity surfacing; `core/validation_cache.go` (opt-in cache + domain health); `core/aifilter` (quick filter + fail-open LLM classifier) with `/api/suppressed` and a Suppressed dashboard tab; `core/notify` (Slack/Email/File/Sound/Discord/Telegram/Generic + policy) wired alongside the webhook queue; `core/gitlab.go` source; config sections `http`/`storage`/`search`/`ai_filter`/`notify`/`sources`/`validators` + `scanning.context_lines` in both config files.

Known gaps (see Deferred/Risks):
- Metrics and proxy remain deferred; region push channels deferred.
- AI-filter daily digest is a log-only summary, not a delivered message.
- TokenResult `latency_ms` is present on the wire but unpopulated.
- TUI-created AI reviews do not carry base URL/severity (only dashboard-created reviews do).
- The registry is the dispatch path for detected providers with a legacy fallback; the full removal of every legacy switch arm is not complete.
- `serverchan`/`pushplus`/`feishu`/`dingtalk`/`bark` config entries are not registered (deferred).
