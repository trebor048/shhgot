package core

import "time"

// Validation-cache defaults for the "validators:" section (Slice 7). Caching is
// off unless cache_enabled is set, a cached outcome lives an hour, at most ten
// thousand entries are retained, and a dead host is skipped for half an hour.
const (
	defaultValidatorCacheTTL     = time.Hour
	defaultValidatorCacheEntries = 10000
	defaultValidatorCooldown     = 30 * time.Minute
	defaultValidatorConcurrency  = 8
)

// NewValidationCache maps the "validators:" section of config.yaml onto a
// ValidationCache. Caching is disabled unless cache_enabled is true, so an
// existing operator's request volume is unchanged until they opt in.
func (c *Config) NewValidationCache() *ValidationCache {
	cfg := CacheConfig{
		TTL:        defaultValidatorCacheTTL,
		MaxEntries: defaultValidatorCacheEntries,
		Cooldown:   defaultValidatorCooldown,
	}
	if c == nil {
		return NewValidationCache(cfg)
	}
	v := c.Validators
	cfg.Enabled = boolOr(v.CacheEnabled, false)
	cfg.TTL = secondsOr(v.CacheTTLSecs, cfg.TTL)
	cfg.MaxEntries = intOr(v.CacheMaxEntries, cfg.MaxEntries)
	cfg.Cooldown = secondsOr(v.DomainCooldownSecs, cfg.Cooldown)
	return NewValidationCache(cfg)
}

// ValidatorConcurrency returns how many credential validations may run at once,
// from validators.max_concurrency, defaulting to 8.
func (c *Config) ValidatorConcurrency() int {
	if c == nil {
		return defaultValidatorConcurrency
	}
	return intOr(c.Validators.MaxConcurrency, defaultValidatorConcurrency)
}

// ValidatorMinInterval returns the minimum spacing between validation requests
// to the same host, from validators.min_interval_ms, defaulting to the
// historical token-validator interval.
func (c *Config) ValidatorMinInterval() time.Duration {
	def := time.Duration(defaultValidatorMinIntervalMS) * time.Millisecond
	if c == nil {
		return def
	}
	return msOr(c.Validators.MinIntervalMS, def)
}
