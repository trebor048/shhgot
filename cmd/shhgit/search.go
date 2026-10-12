package main

import (
	"bufio"
	"bytes"
	"math"
	"os"
	"strings"
	"time"

	"github.com/trebor048/shhgot/core"
)

// Code-search source seams. They are package variables so tests can point the
// search API and the download path at httptest servers; production leaves them
// empty/nil, which selects the public GitHub API and the shared resilient
// client.
var (
	codeSearchBaseURL      string
	codeSearchDoer         core.Doer
	codeSearchAllowedHosts []string
)

// maxFileSizeBytes converts a KB setting to bytes, clamping before the
// conversion so a huge --maximum-file-size cannot overflow int64.
func maxFileSizeBytes(kb uint) int64 {
	const maxKB = math.MaxInt64 / 1024
	if kb > maxKB {
		return math.MaxInt64
	}
	return int64(kb) * 1024
}

// codeSearchEnabled reports whether the search source should run, honouring
// both the config section and the --search override.
func codeSearchEnabled() bool {
	return getSession().Config.SearchEnabled() || boolOpt(getSession().Options.Search)
}

// startCodeSearch launches the code-search source in the background when it is
// enabled. It is a remote-scan feature only: a --local scan needs no token, and
// the one-shot commands must not start a background network source.
func startCodeSearch() {
	if !codeSearchEnabled() {
		return
	}
	// sourceStartBlocked covers --local and every one-shot command (including
	// --stats), so this source cannot drift from the other remote sources.
	if sourceStartBlocked() {
		return
	}
	go runCodeSearch()
}

// runCodeSearch runs the GitHub code-search (dork) discovery source: it issues
// each query, downloads every allowlisted hit through core.Fetcher, and feeds
// the body to the shared scanContents engine so search findings are produced by
// exactly the same signatures and suppression rules as a cloned repository.
func runCodeSearch() {
	log := getSession().Log

	token := firstGitHubToken()
	if token == "" {
		msg := "GitHub code search is enabled but no GitHub access token is configured; code search requires a token with the code-search scope"
		log.Warn("[search] %s", msg)
		lockPrintf("[!] %s\n", msg)
		return
	}

	queries := configuredSearchQueries()
	if len(queries) == 0 {
		queries = core.BuildDorks(core.SignatureAnchors(getSession().Signatures))
	}
	if len(queries) == 0 {
		msg := "GitHub code search is enabled but no queries were available; set search.queries or add signatures with literals"
		log.Warn("[search] %s", msg)
		lockPrintf("[!] %s\n", msg)
		return
	}

	_, perPage, maxPages, maxResults, sleepMS, minStars := getSession().Config.SearchValues()
	o := getSession().Options
	if o.SearchMaxResults != nil && *o.SearchMaxResults > 0 {
		maxResults = *o.SearchMaxResults
	}
	if o.SearchSleepMS != nil && *o.SearchSleepMS > 0 {
		sleepMS = *o.SearchSleepMS
	}

	doer := codeSearchDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	fetcher := &core.Fetcher{
		Doer:         doer,
		MaxFileSize:  maxFileSizeBytes(*o.MaximumFileSize),
		AllowedHosts: codeSearchAllowedHosts,
	}
	opts := core.CodeSearchOptions{
		PerPage:    perPage,
		MaxPages:   maxPages,
		MaxResults: maxResults,
		SleepMS:    sleepMS,
		Token:      token,
		BaseURL:    codeSearchBaseURL,
	}

	log.Info("[search] GitHub code search: %d queries, up to %d results each", len(queries), maxResults)
	for _, q := range queries {
		// SearchCode returns the hits it collected before a mid-pagination
		// failure alongside the error, so process those partial hits first and
		// only then report the error; discarding them lost an entire page of
		// findings on any transient failure.
		start := time.Now()
		hits, err := core.SearchCode(doer, opts, q)
		if core.MetricsCollectionEnabled() {
			core.DefaultMetrics.ObserveLatency(core.SubsystemSearch, time.Since(start))
			core.DefaultMetrics.IncOperations(core.SubsystemSearch)
			if err != nil {
				core.DefaultMetrics.IncError(core.SubsystemSearch)
			}
		}
		for _, hit := range hits {
			if minStars > 0 && hit.Stars < minStars {
				continue
			}
			// The extension/path blacklist is applied before the download so a
			// blacklisted path costs no request; scanContents re-checks it.
			if core.IsSkippableFile(hit.Path) {
				continue
			}
			body, ferr := fetcher.Fetch(hit.RawURL)
			if ferr != nil {
				log.Debug("[search] skipping %s: %s", hit.RawURL, ferr)
				continue
			}
			scanContents(searchRepoURL(hit.Repo), "", hit.Path, hit.HTMLURL, hit.Stars, body, core.GITHUB_SEARCH_SOURCE)
		}
		if err != nil {
			log.Warn("[search] query %q failed: %s", q, err)
		}
	}
}

// firstGitHubToken returns the first non-empty configured GitHub token, or "".
func firstGitHubToken() string {
	for _, t := range getSession().GitHubTokens() {
		if s := strings.TrimSpace(t); s != "" {
			return s
		}
	}
	return ""
}

// configuredSearchQueries returns the operator's explicit dork list: the
// search.queries entries plus, when set, one query per non-blank non-comment
// line of search.query_file. An empty result means the caller derives dorks
// from the loaded signatures.
func configuredSearchQueries() []string {
	cfg := getSession().Config
	out := make([]string, 0, len(cfg.Search.Queries))
	for _, q := range cfg.Search.Queries {
		if s := strings.TrimSpace(q); s != "" {
			out = append(out, s)
		}
	}
	if file := strings.TrimSpace(cfg.Search.QueryFile); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			getSession().Log.Warn("[search] cannot read search.query_file %s: %s", file, err)
		} else {
			for _, line := range strings.Split(string(data), "\n") {
				s := strings.TrimSpace(line)
				if s == "" || strings.HasPrefix(s, "#") {
					continue
				}
				out = append(out, s)
			}
		}
	}
	return out
}

// searchRepoURL turns a code-search repository slug into the web URL the
// finding link is built from. A value that is already a URL is returned
// unchanged. GitHub's code search also returns gist hits whose slug is
// host-prefixed ("gist.github.com/<id>"), so a slug whose first segment is a
// hostname gets only the scheme added - never a second "github.com/".
func searchRepoURL(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" || strings.Contains(repo, "://") {
		return repo
	}
	repo = strings.TrimSuffix(repo, ".git")
	if i := strings.IndexByte(repo, '/'); i > 0 && strings.Contains(repo[:i], ".") {
		return "https://" + repo
	}
	return "https://github.com/" + repo
}

// scanBinaryEnabled reports whether scanning.scan_binary re-enables binary-file
// scanning. scanContents consults it so the documented escape hatch is not
// defeated by an unconditional binary gate.
func scanBinaryEnabled() bool {
	c := getSession().Config
	if c == nil {
		return false
	}
	return c.Scanning.Bool(c.Scanning.ScanBinary, false)
}

// scanContents applies every in-scope signature to one body of file contents
// and publishes each finding. It is the single per-file matching engine behind
// both the on-disk scan (checkSignatures) and the GitHub code-search source
// (runCodeSearch), so the two paths cannot drift in what they flag.
//
// repo is the repository URL or "owner/name"; branch is the ref used for blob
// links; path is the repo-relative display path; htmlURL is the canonical web
// URL of the file, used when repo is empty; stars is the repository star count
// (-1 when unknown); source tags the published finding.
//
// It returns true when at least one finding was published for this file.
func scanContents(repo, branch, path, htmlURL string, stars int, contents []byte, source core.GitResourceType) (matchedAny bool) {
	o := getSession().Options

	// Size gate. The on-disk walk already applies it and the Fetcher caps the
	// download, but a downloaded hit reaches this function directly, so it is
	// the one place that must apply it.
	if max := maxFileSizeBytes(uintOpt(o.MaximumFileSize)); max > 0 && int64(len(contents)) > max {
		return false
	}
	// Binary files are skipped unless scanning.scan_binary re-enables them. The
	// gate applies to content signatures only: a path/name/extension rule is
	// still evaluated for a binary or empty file, matching the on-disk walk.
	binarySkip := core.IsBinaryContent(contents) && !scanBinaryEnabled()
	displayPath := path
	if displayPath == "" {
		displayPath = htmlURL
	}
	if core.IsSkippableFile(displayPath) {
		return false
	}

	file := core.NewMatchFileFromContents(displayPath, contents)
	file.Repo = repo
	file.EnableLowerCache()
	// Env files whose only assignments are public VITE_* variables are not
	// findings. checkSignatures applies the same filter before calling here so a
	// --search-query scan keeps its previous behaviour.
	if isViteOnlyEnvFile(file) {
		return false
	}

	url := repo
	if strings.TrimSpace(url) == "" {
		url = htmlURL
	}

	for _, signature := range getSession().Signatures {
		if isSignatureExcludedInMode(signature, currentRunMode()) {
			continue
		}
		matched, part := signature.Match(file)
		if !matched {
			continue
		}
		if part == core.PartContents && (len(contents) == 0 || binarySkip) {
			continue
		}

		if part != core.PartContents {
			if *o.PathChecks {
				publish(&MatchEvent{Source: source, Url: url, Signature: signature.Name(), File: displayPath, Stars: stars, Priority: signature.GetPriority(), Color: signature.GetColor(), FileContent: string(contents)})
				matchedAny = true
				logFile(url, signature.Name(), displayPath, branch, stars)
				getSession().WriteToCsv([]string{url, signature.Name(), displayPath, ""})
				getSession().LogMatch(signature.Name(), url, displayPath, []string{displayPath})
			}
			if !binarySkip && file.CanCheckEntropy() && scanEntropy(url, displayPath, branch, contents, stars, source) {
				matchedAny = true
			}
			continue
		}

		matches := signature.GetContentsMatches(file)
		if len(matches) == 0 {
			continue
		}
		// Test fixtures (e.g. backend/tests/*) overwhelmingly contain fake
		// placeholder passwords - skip them.
		if isPasswordFamilySignature(signature.Name()) && isTestFixturePath(displayPath) {
			continue
		}
		// AI-agent skill/instruction files are documentation: any secret shown
		// inside them is an example, not a finding.
		if isDocSkillPath(displayPath) {
			continue
		}
		// A PEM header with no closing footer is a doc fragment, not a key.
		if strings.HasPrefix(matches[0], "-----BEGIN") && !hasCompletePemBlock(string(contents)) {
			continue
		}

		secret := matches[0]
		secretLine := secretLineNum(contents, secret)
		// Validate before publishing. For an AI signature that has no
		// verifier: entry this is the only verification the finding gets;
		// running it after publish meant the webhook colour and the stored
		// record carried the priority-fallback severity while only the
		// dashboard was later corrected.
		if isAITokenSignature(strings.ToLower(signature.Name())) {
			getSession().TokenValidator.TestMatchedTokens(matches, signature.GetColor())
		}
		publish(&MatchEvent{Source: source, Url: url, Matches: matches, Signature: signature.Name(), File: displayPath, Stars: stars, Priority: signature.GetPriority(), Color: signature.GetColor(), FileContent: string(contents), Secret: secret, SecretLine: secretLine})
		matchedAny = true
		logSecret(len(matches), url, signature.Name(), displayPath, strings.Join(matches, ", "), branch, secretLine, stars)
		getSession().WriteToCsv([]string{url, signature.Name(), displayPath, strings.Join(matches, ", ")})
		getSession().LogMatch(signature.Name(), url, displayPath, matches)

		if signature.IsTokenType() {
			for _, token := range matches {
				getSession().AddGitHubToken(token)
			}
		}
	}
	return matchedAny
}

// scanEntropy runs the high-entropy line heuristic over contents, publishing
// each line above the configured threshold. It mirrors the loop the on-disk
// scan used inline, so a downloaded code-search hit is measured exactly like a
// cloned file.
func scanEntropy(url, file, branch string, contents []byte, stars int, source core.GitResourceType) (matchedAny bool) {
	threshold := floatOpt(getSession().Options.EntropyThreshold)
	if threshold <= 0 {
		return false
	}

	lowerBlacklist := make([]string, 0, len(getSession().Config.BlacklistedStrings))
	for _, b := range getSession().Config.BlacklistedStrings {
		if b != "" {
			lowerBlacklist = append(lowerBlacklist, strings.ToLower(b))
		}
	}

	scanner := bufio.NewScanner(bytes.NewReader(contents))
	// Raise the per-line cap above bufio's 64 KiB default so a minified bundle
	// or long data URI does not abort the scan, and surface the error below.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if len(line) <= 6 || len(line) >= 100 {
			continue
		}
		if core.GetEntropy(line) < threshold {
			continue
		}
		lowerLine := strings.ToLower(line)
		blacklisted := false
		for _, b := range lowerBlacklist {
			if strings.Contains(lowerLine, b) {
				blacklisted = true
				break
			}
		}
		if blacklisted {
			continue
		}
		publish(&MatchEvent{Source: source, Url: url, Matches: []string{line}, Signature: entropySignatureName, File: file, Stars: stars, Priority: 0, Color: "", FileContent: string(contents), Secret: line, SecretLine: lineNo})
		matchedAny = true
		logEntropy(url, file, line, branch, lineNo, stars)
		getSession().WriteToCsv([]string{url, entropySignatureName, file, line})
		getSession().LogMatch(entropySignatureName, url, file, []string{line})
	}
	if err := scanner.Err(); err != nil && getSession().Log != nil {
		// A line over the buffer cap truncates the scan; make that visible
		// instead of silently skipping the rest of the file.
		getSession().Log.Debug("entropy scan truncated for %s: %v", file, err)
	}
	return matchedAny
}
