package core

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core/verifiers"
)

// countingDoer is a verifiers.Doer test double: it counts the requests it is
// handed and answers with a canned response (or a transport error) so the cache
// and domain-health paths can be exercised without touching the network.
type countingDoer struct {
	mu      sync.Mutex
	calls   int
	status  int
	body    string
	headers map[string]string
	err     error
}

func (d *countingDoer) Do(*http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	h := make(http.Header)
	for k, v := range d.headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(d.body)),
	}, nil
}

func (d *countingDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func openAIKey(seed byte) string {
	return "sk-proj-" + strings.Repeat(string(seed), 40)
}

// TestValidationCacheSkipsRepeatedProviderCall proves the second validator (an
// empty in-memory testedTokens map, as a new run would have) is answered from
// the shared ValidationCache, so the provider sees exactly one request.
func TestValidationCacheSkipsRepeatedProviderCall(t *testing.T) {
	doer := &countingDoer{body: `{"data":[{"id":"gpt-4o"}]}`}
	cache := NewValidationCache(CacheConfig{Enabled: true})
	token := openAIKey('a')

	first := NewTokenValidator(nil)
	first.httpClient = doer
	first.Cache = cache
	if !first.TestAndLogToken(token, "") {
		t.Fatal("first validation should report the key valid")
	}
	if got := doer.count(); got != 1 {
		t.Fatalf("provider called %d times after first validation, want 1", got)
	}

	second := NewTokenValidator(nil)
	second.httpClient = doer
	second.Cache = cache
	if !second.TestAndLogToken(token, "") {
		t.Fatal("cached validation should report the key valid")
	}
	if got := doer.count(); got != 1 {
		t.Fatalf("provider called %d times, want 1 (cache hit within TTL)", got)
	}
}

// TestTokenOutcomeDrivesSeverity checks the enriched tokenTestResult carries the
// verifier Outcome and that Classify consumes it.
func TestTokenOutcomeDrivesSeverity(t *testing.T) {
	doer := &countingDoer{body: `{"data":[{"id":"gpt-4o"}]}`}
	tv := NewTokenValidator(nil)
	tv.httpClient = doer
	token := openAIKey('b')
	if !tv.TestAndLogToken(token, "") {
		t.Fatal("validation should succeed")
	}

	tv.mutex.Lock()
	res := tv.testedTokens[token]
	tv.mutex.Unlock()

	if res.outcome.Status != verifiers.Valid {
		t.Fatalf("outcome status = %q, want %q", res.outcome.Status, verifiers.Valid)
	}
	if res.outcome.Tier != "GPT-4" {
		t.Fatalf("outcome tier = %q, want GPT-4", res.outcome.Tier)
	}
	if got := Classify(0, &res.outcome); got != SevHigh {
		t.Fatalf("Classify of live top-tier unfunded outcome = %s, want high", got)
	}
	funded := verifiers.Outcome{Status: verifiers.Valid, Tier: "GPT-4", Balance: "$150"}
	if got := Classify(0, &funded); got != SevCritical {
		t.Fatalf("Classify of funded top-tier outcome = %s, want critical", got)
	}
}

// TestDeadHostShortCircuitsAfterFailureThreshold drives a non-protected host
// (api.github.com) to the dead threshold, then asserts the next key is answered
// without any request reaching the provider.
func TestDeadHostShortCircuitsAfterFailureThreshold(t *testing.T) {
	cache := NewValidationCache(CacheConfig{Enabled: true})
	doer := &countingDoer{err: errors.New("connection refused")}
	tv := NewTokenValidator(nil)
	tv.httpClient = doer
	tv.Cache = cache

	const host = "api.github.com"
	for i := 0; i < domainDeadAt; i++ {
		token := fmt.Sprintf("ghp_%036d", i)
		if tv.TestAndLogToken(token, "") {
			t.Fatalf("transport error must not validate (token %d)", i)
		}
	}
	if !cache.DomainDead(host) {
		t.Fatalf("host %s should be dead after %d failures", host, domainDeadAt)
	}

	before := doer.count()
	if tv.TestAndLogToken("ghp_"+strings.Repeat("9", 36), "") {
		t.Fatal("a dead host must not validate")
	}
	if got := doer.count(); got != before {
		t.Fatalf("dead host received %d requests, want 0 more", got-before)
	}
}

// TestProtectedHostStillReceivesRequests proves the built-in AI host
// (api.openai.com) is exempt from dead-host short-circuiting: every key still
// reaches the provider while a different host is dead.
func TestProtectedHostStillReceivesRequests(t *testing.T) {
	cache := NewValidationCache(CacheConfig{Enabled: true})
	doer := &countingDoer{err: errors.New("boom")}
	tv := NewTokenValidator(nil)
	tv.httpClient = doer
	tv.Cache = cache

	for i := 0; i < domainDeadAt+5; i++ {
		tv.TestAndLogToken(openAIKey(byte('0'+i%10))+fmt.Sprintf("%d", i), "")
	}
	if got := doer.count(); got != domainDeadAt+5 {
		t.Fatalf("protected host received %d requests, want %d", got, domainDeadAt+5)
	}
	if cache.DomainDead("api.openai.com") {
		t.Fatal("a protected AI host must never be marked dead")
	}
}

// TestInitTokenValidatorWiresConfig proves the "validators:" section actually
// reaches the runtime validator: before this the cache was nil and every
// validators.* key was a no-op, and max_concurrency bounded nothing.
func TestInitTokenValidatorWiresConfig(t *testing.T) {
	enabled := true
	concurrency := 3
	s := &Session{Log: &Logger{}, Config: &Config{Validators: ValidatorsConfig{
		CacheEnabled:   &enabled,
		MaxConcurrency: &concurrency,
	}}}
	s.InitTokenValidator()

	if s.TokenValidator.Cache == nil {
		t.Fatal("InitTokenValidator left the validation cache nil")
	}
	if !s.TokenValidator.Cache.enabled {
		t.Fatal("validators.cache_enabled=true did not enable the cache")
	}
	if got := cap(s.TokenValidator.sem); got != concurrency {
		t.Fatalf("concurrency semaphore cap = %d, want %d", got, concurrency)
	}
}

// blockingDoer blocks inside Do until release is closed, signalling each start
// on started. It lets a test observe how many validations run at once.
type blockingDoer struct {
	started chan struct{}
	release chan struct{}
}

func (d *blockingDoer) Do(*http.Request) (*http.Response, error) {
	d.started <- struct{}{}
	<-d.release
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-4o"}]}`)),
	}, nil
}

// TestTokenValidatorConcurrencyLimitSerialises proves validators.max_concurrency
// bounds how many validations run at once.
func TestTokenValidatorConcurrencyLimitSerialises(t *testing.T) {
	doer := &blockingDoer{started: make(chan struct{}, 8), release: make(chan struct{})}
	tv := NewTokenValidator(nil)
	tv.httpClient = doer
	tv.SetConcurrency(1)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tv.TestAndLogToken(openAIKey(byte('a'+i)), "")
		}(i)
	}

	select {
	case <-doer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("no validation started")
	}
	select {
	case <-doer.started:
		t.Fatal("a second validation started while max_concurrency is 1")
	case <-time.After(50 * time.Millisecond):
	}
	close(doer.release)
	wg.Wait()
}

// TestConcurrentDuplicateWaitsForVerdict is a regression test for the reserved
// -entry bug: TestAndLogToken used to store {valid:true} while a validation was
// in flight, so a concurrent duplicate was told the key was valid before the
// provider had answered. It must instead wait for (and return) the real verdict.
func TestConcurrentDuplicateWaitsForVerdict(t *testing.T) {
	doer := &blockingDoer{started: make(chan struct{}, 1), release: make(chan struct{})}
	tv := NewTokenValidator(nil)
	tv.httpClient = doer
	token := openAIKey('e')

	first := make(chan bool, 1)
	go func() { first <- tv.TestAndLogToken(token, "") }()

	select {
	case <-doer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("validation never started")
	}

	second := make(chan bool, 1)
	go func() { second <- tv.TestAndLogToken(token, "") }()

	select {
	case v := <-second:
		t.Fatalf("duplicate returned %v before the in-flight verdict", v)
	case <-time.After(75 * time.Millisecond):
	}

	close(doer.release)

	if v := <-first; !v {
		t.Fatal("first validation should report the key valid")
	}
	select {
	case v := <-second:
		if !v {
			t.Fatal("duplicate should share the eventual valid verdict")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("duplicate did not return after the verdict")
	}
}

// TestValidationCachePreservesRPM is a regression test for the cached-outcome
// projection dropping RPM: a cache hit must carry the same rate limit as the
// live response did.
func TestValidationCachePreservesRPM(t *testing.T) {
	doer := &countingDoer{
		body:    `{"data":[{"id":"gpt-4o"}]}`,
		headers: map[string]string{"x-ratelimit-limit-requests": "777"},
	}
	cache := NewValidationCache(CacheConfig{Enabled: true})
	token := openAIKey('r')

	live := NewTokenValidator(nil)
	live.httpClient = doer
	live.Cache = cache
	if !live.TestAndLogToken(token, "") {
		t.Fatal("first validation should succeed")
	}
	live.mutex.Lock()
	liveRPM := live.testedTokens[token].outcome.RPM
	live.mutex.Unlock()
	if liveRPM != 777 {
		t.Fatalf("live outcome RPM = %d, want 777", liveRPM)
	}

	// A fresh validator (empty in-memory map, as a new run would have) is
	// answered from the shared ValidationCache; RPM must survive the round trip.
	cached := NewTokenValidator(nil)
	cached.httpClient = doer
	cached.Cache = cache
	if !cached.TestAndLogToken(token, "") {
		t.Fatal("cached validation should succeed")
	}
	cached.mutex.Lock()
	cachedRPM := cached.testedTokens[token].outcome.RPM
	cached.mutex.Unlock()
	if cachedRPM != 777 {
		t.Fatalf("cached outcome RPM = %d, want 777 (RPM lost on cache hit)", cachedRPM)
	}
	if got := doer.count(); got != 1 {
		t.Fatalf("provider called %d times, want 1 (cache hit)", got)
	}
}

// TestConfigNewValidationCacheDefaults pins the documented defaults for an
// absent validators: section.
func TestConfigNewValidationCacheDefaults(t *testing.T) {
	var cfg *Config
	cache := cfg.NewValidationCache()
	if cache == nil {
		t.Fatal("NewValidationCache returned nil")
	}
	if cache.enabled {
		t.Fatal("caching must be disabled by default")
	}
	if cache.ttl != time.Hour {
		t.Fatalf("ttl = %s, want 1h", cache.ttl)
	}
	if cache.cooldown != 30*time.Minute {
		t.Fatalf("cooldown = %s, want 30m", cache.cooldown)
	}
	if cache.shards[0].cap != defaultValidatorCacheEntries/validationCacheShards {
		t.Fatalf("per-shard cap = %d, want %d", cache.shards[0].cap, defaultValidatorCacheEntries/validationCacheShards)
	}
	if cfg.ValidatorConcurrency() != defaultValidatorConcurrency {
		t.Fatalf("concurrency = %d, want %d", cfg.ValidatorConcurrency(), defaultValidatorConcurrency)
	}
	if cfg.ValidatorMinInterval() != time.Duration(defaultValidatorMinIntervalMS)*time.Millisecond {
		t.Fatalf("min interval = %s, want %dms", cfg.ValidatorMinInterval(), defaultValidatorMinIntervalMS)
	}
}

// TestConfigNewValidationCacheOverrides pins that an explicit validators:
// section wins over the defaults.
func TestConfigNewValidationCacheOverrides(t *testing.T) {
	enabled := true
	ttl := 120
	maxEntries := 320
	cooldown := 60
	concurrency := 3
	interval := 250
	cfg := &Config{Validators: ValidatorsConfig{
		CacheEnabled:       &enabled,
		CacheTTLSecs:       &ttl,
		CacheMaxEntries:    &maxEntries,
		DomainCooldownSecs: &cooldown,
		MaxConcurrency:     &concurrency,
		MinIntervalMS:      &interval,
	}}

	cache := cfg.NewValidationCache()
	if !cache.enabled {
		t.Fatal("cache_enabled true must enable caching")
	}
	if cache.ttl != 120*time.Second {
		t.Fatalf("ttl = %s, want 120s", cache.ttl)
	}
	if cache.cooldown != 60*time.Second {
		t.Fatalf("cooldown = %s, want 60s", cache.cooldown)
	}
	if cache.shards[0].cap != maxEntries/validationCacheShards {
		t.Fatalf("per-shard cap = %d, want %d", cache.shards[0].cap, maxEntries/validationCacheShards)
	}
	if cfg.ValidatorConcurrency() != concurrency {
		t.Fatalf("concurrency = %d, want %d", cfg.ValidatorConcurrency(), concurrency)
	}
	if cfg.ValidatorMinInterval() != 250*time.Millisecond {
		t.Fatalf("min interval = %s, want 250ms", cfg.ValidatorMinInterval())
	}
}

// TestOnOutcomeCallbackFiresAndOnTokenResultUnchanged guards the additive
// callback contract: both fire, with the same masked token, and OnTokenResult
// keeps its original signature.
func TestOnOutcomeCallbackFiresAndOnTokenResultUnchanged(t *testing.T) {
	doer := &countingDoer{body: `{"data":[{"id":"gpt-4o"}]}`}
	tv := NewTokenValidator(nil)
	tv.httpClient = doer

	var (
		resultValid bool
		resultProv  string
		outcomeProv string
		outcomeSt   verifiers.Status
	)
	tv.OnTokenResult = func(_ string, valid bool, provider string) {
		resultValid, resultProv = valid, provider
	}
	tv.OnOutcome = func(_ string, provider string, o verifiers.Outcome) {
		outcomeProv, outcomeSt = provider, o.Status
	}

	if !tv.TestAndLogToken(openAIKey('c'), "") {
		t.Fatal("validation should succeed")
	}
	if !resultValid || resultProv != ProviderOpenAI {
		t.Fatalf("OnTokenResult got valid=%v provider=%q", resultValid, resultProv)
	}
	if outcomeProv != ProviderOpenAI || outcomeSt != verifiers.Valid {
		t.Fatalf("OnOutcome got provider=%q status=%q", outcomeProv, outcomeSt)
	}
}
