package core

// Defaults for the "search:" section of config.yaml. They are consulted only
// when the corresponding key is absent; an explicit positive value wins.
//
// max_pages defaults to 1 rather than codesearch.go's library default of 10:
// the search source is opt-in and a single page of 100 hits is the conservative
// first run, while the operator can widen it deliberately.
const (
	defaultSearchConfigPerPage    = 100
	defaultSearchConfigMaxPages   = 1
	defaultSearchConfigMaxResults = 1000
	defaultSearchConfigSleepMS    = 2000
)

// SearchEnabled reports whether the GitHub code-search discovery source is
// enabled. It is off unless search.enabled is explicitly true, so an operator
// who has not opted in keeps the previous behaviour.
func (c *Config) SearchEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Search.Enabled, false)
}

// SearchValues resolves the "search:" section into the values runCodeSearch
// consumes. Absent keys keep the documented defaults: perPage 100, maxPages 1,
// maxResults 1000, sleepMS 2000 and minStars 0. queries is the configured dork
// list; it is returned nil when unset so the caller can derive dorks from the
// loaded signatures instead.
func (c *Config) SearchValues() (queries []string, perPage, maxPages, maxResults, sleepMS, minStars int) {
	if c == nil {
		return nil, defaultSearchConfigPerPage, defaultSearchConfigMaxPages,
			defaultSearchConfigMaxResults, defaultSearchConfigSleepMS, 0
	}
	return c.Search.Queries,
		intOr(c.Search.PerPage, defaultSearchConfigPerPage),
		intOr(c.Search.MaxPages, defaultSearchConfigMaxPages),
		intOr(c.Search.MaxResults, defaultSearchConfigMaxResults),
		intOr(c.Search.SleepMS, defaultSearchConfigSleepMS),
		intOr(c.Search.MinStars, 0)
}
