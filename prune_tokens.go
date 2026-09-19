package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/eth0izzle/shhgit/core"
)

// GitHub token pruning.
//
// On every startup (non-local mode) each token in github_access_tokens is
// checked against the GitHub API and the ones GitHub definitively reports as
// invalid (HTTP 401 — revoked/expired) are removed from config.yaml, with a
// backup written first. Only a definite 401 removes a token: rate limits,
// server errors, network failures, and empty env-var expansions keep the token
// so a transient problem never deletes a still-valid credential.
//
// The same rule is applied *while running*: when a live API call (events,
// gists, repository lookup) comes back 401, core calls the TokenPersistRemover
// hook installed by installRuntimeTokenPruner, which deletes the token from
// config.yaml on the spot.

type tokenVerdict int

const (
	verdictValid     tokenVerdict = iota
	verdictRevoked                // HTTP 401 — definitely revoked/expired
	verdictAmbiguous              // 403/5xx/network/empty — cannot decide, keep
)

// pruneHTTPClient and pruneGitHubUserURL are vars so tests can point them at a
// local httptest server.
var (
	pruneHTTPClient    = http.DefaultClient
	pruneGitHubUserURL = "https://api.github.com/user"
)

// checkGitHubToken reports a single token's verdict against the GitHub API.
// The User-Agent header is required — GitHub rejects requests without one.
func checkGitHubToken(token string) tokenVerdict {
	if strings.TrimSpace(token) == "" {
		return verdictAmbiguous
	}
	req, err := http.NewRequest(http.MethodGet, pruneGitHubUserURL, nil)
	if err != nil {
		return verdictAmbiguous
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "shhgit-token-prune")
	resp, err := pruneHTTPClient.Do(req)
	if err != nil {
		return verdictAmbiguous
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // drain so the conn can be reused
	switch resp.StatusCode {
	case http.StatusOK:
		return verdictValid
	case http.StatusUnauthorized:
		return verdictRevoked
	default:
		return verdictAmbiguous
	}
}

// stripYAMLQuotes removes surrounding single/double quotes from a raw scalar
// (config.yaml entries often quote their token values).
func stripYAMLQuotes(value string) string {
	if len(value) >= 2 {
		if (value[0] == '\'' && value[len(value)-1] == '\'') ||
			(value[0] == '"' && value[len(value)-1] == '"') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// classifyTokens checks each raw config value (after env expansion) and
// returns which raw values are definitively revoked, plus counts of kept and
// ambiguous tokens. Raw values are returned so they can be matched against the
// config file lines (which may be $ENV references or quoted rather than
// literals).
func classifyTokens(rawValues []string) (removed map[string]bool, kept, ambiguous int) {
	removed = make(map[string]bool)
	for _, raw := range rawValues {
		switch checkGitHubToken(os.ExpandEnv(stripYAMLQuotes(raw))) {
		case verdictValid:
			kept++
		case verdictRevoked:
			removed[raw] = true
		default:
			ambiguous++
		}
	}
	return removed, kept, ambiguous
}

// ghTokenLine is one "- value" line under the github_access_tokens: block.
type ghTokenLine struct {
	index int    // index into the split line slice
	value string // raw value after the "- "
}

// scanGitHubTokenBlock finds the top-level github_access_tokens: key and
// collects its list items. Blank lines and comments inside the block are
// skipped but do not end it; the first non-item, non-comment line ends it.
func scanGitHubTokenBlock(lines []string) []ghTokenLine {
	var items []ghTokenLine
	inBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if trimmed == "github_access_tokens:" &&
				!strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				inBlock = true
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // keep scanning; block may continue after comments/blank lines
		}
		if strings.HasPrefix(trimmed, "-") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if idx := strings.Index(value, " #"); idx >= 0 {
				value = value[:idx]
			}
			value = strings.TrimSpace(value)
			// Strip YAML quoting so the raw token sent to GitHub has no quotes
			// (config files commonly write '- 'ghp_...'').
			value = stripYAMLQuotes(value)
			if value != "" {
				items = append(items, ghTokenLine{index: i, value: value})
			}
			continue
		}
		inBlock = false // a new key (or anything else) ends the block
	}
	return items
}

// extractGitHubTokenLines returns the raw token values under
// github_access_tokens: in the config file.
func extractGitHubTokenLines(configPath string) ([]string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	items := scanGitHubTokenBlock(strings.Split(string(data), "\n"))
	values := make([]string, 0, len(items))
	for _, it := range items {
		values = append(values, it.value)
	}
	return values, nil
}

// rewriteConfigRemovingTokens surgically drops the "- value" lines whose raw
// value is in removed, leaving every other byte of the file untouched. A
// backup (configPath+".bak") is written before the edit, and only when
// something is actually removed. Returns the number of lines removed.
func rewriteConfigRemovingTokens(configPath string, removed map[string]bool) (int, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	items := scanGitHubTokenBlock(lines)

	drop := make(map[int]bool)
	for _, it := range items {
		if removed[it.value] {
			drop[it.index] = true
		}
	}
	if len(drop) == 0 {
		return 0, nil
	}

	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if drop[i] {
			continue
		}
		out = append(out, line)
	}

	if err := os.WriteFile(configPath+".bak", data, 0o644); err != nil {
		return len(drop), err
	}
	return len(drop), os.WriteFile(configPath, []byte(strings.Join(out, "\n")), 0o644)
}

// matchTokenLines returns the raw config values (as produced by
// extractGitHubTokenLines) whose env-expanded form equals token. Config entries
// may be literals, $ENV references, or quoted; only the expanded value is
// comparable to the token a client actually used.
func matchTokenLines(rawValues []string, token string) map[string]bool {
	removed := make(map[string]bool)
	for _, raw := range rawValues {
		if os.ExpandEnv(raw) == token {
			removed[raw] = true
		}
	}
	return removed
}

// configWriteMu serializes config.yaml edits so concurrent 401s for different
// tokens cannot interleave their read-modify-write cycles and lose a removal.
var configWriteMu sync.Mutex

// removeTokenFromConfigFile deletes every config.yaml line under
// github_access_tokens: whose env-expanded value equals token. Returns the
// number of lines removed (0 when the token was not in the file, e.g. it came
// from an env var that is not referenced literally).
func removeTokenFromConfigFile(token string) (int, error) {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()

	s := getSession()
	cfgPath := core.ConfigFilePath(s.Options)
	if cfgPath == "" {
		return 0, fmt.Errorf("could not locate config.yaml")
	}

	rawTokens, err := extractGitHubTokenLines(cfgPath)
	if err != nil {
		return 0, err
	}

	removed := matchTokenLines(rawTokens, token)
	if len(removed) == 0 {
		return 0, nil
	}
	return rewriteConfigRemovingTokens(cfgPath, removed)
}

// installRuntimeTokenPruner wires core's mid-run 401 handler to config.yaml
// editing so a token revoked while the scanner is running is deleted from the
// file immediately (with a .bak backup), not only on the next startup. Call
// once before the scanner starts.
func installRuntimeTokenPruner() {
	core.TokenPersistRemover = func(token string) error {
		n, err := removeTokenFromConfigFile(token)
		if err != nil {
			return err
		}
		if n > 0 {
			lockPrintf("[!] GitHub token %s[..] got 401 (bad credentials) — removed %d line(s) from config.yaml\n",
				core.MaskToken(token), n)
		}
		return nil
	}
}

// pruneInvalidGitHubTokens is the startup entry point: validate every GitHub
// access token in config.yaml, remove the definitively-revoked ones from the
// file (with a backup) and from the in-memory config, and report. It never
// fails startup — problems are logged and tokens are kept.
func pruneInvalidGitHubTokens() {
	s := getSession()
	if s.Options.Local != nil && len(*s.Options.Local) > 0 {
		return // local scans do not use GitHub tokens
	}

	cfgPath := core.ConfigFilePath(s.Options)
	if cfgPath == "" {
		s.Log.Warn("Could not locate config.yaml; skipping GitHub token pruning")
		return
	}

	rawTokens, err := extractGitHubTokenLines(cfgPath)
	if err != nil {
		s.Log.Warn("Could not read GitHub tokens from %s: %s", cfgPath, err)
		return
	}
	if len(rawTokens) == 0 {
		return
	}

	removed, kept, ambiguous := classifyTokens(rawTokens)
	if len(removed) == 0 {
		lockPrintf("[*] GitHub tokens: %d checked, all valid%s\n", len(rawTokens), pruneAmbiguousNote(ambiguous))
		return
	}

	configWriteMu.Lock()
	n, err := rewriteConfigRemovingTokens(cfgPath, removed)
	configWriteMu.Unlock()
	if err != nil {
		s.Log.Warn("Could not write pruned config.yaml: %s", err)
	} else if n > 0 {
		lockPrintf("[*] Removed %d invalid GitHub token(s) from %s (backup: %s.bak)\n", n, cfgPath, cfgPath)
	}

	// Update the in-memory config so this run only uses valid tokens, and mark
	// the removed ones so the client pool skips them (InitGitHubClients already
	// queued a client per thread for every token before this runs).
	removedExpanded := make(map[string]bool, len(removed))
	for _, raw := range rawTokens {
		if removed[raw] {
			removedExpanded[os.ExpandEnv(raw)] = true
		}
	}
	s.TokenMutex.Lock()
	keptTokens := make([]string, 0, kept)
	for _, t := range s.Config.GitHubAccessTokens {
		if !removedExpanded[t] {
			keptTokens = append(keptTokens, t)
		}
	}
	s.Config.GitHubAccessTokens = keptTokens
	for tok := range removedExpanded {
		s.RemovedTokens[tok] = true
	}
	s.TokenMutex.Unlock()

	if len(keptTokens) == 0 {
		lockPrintf("[!] All GitHub tokens were removed as invalid. Add a valid token to config.yaml or run with --local.\n")
	}
}

func pruneAmbiguousNote(ambiguous int) string {
	if ambiguous > 0 {
		return " (" + strconv.Itoa(ambiguous) + " skipped: rate-limited/offline)"
	}
	return ""
}
