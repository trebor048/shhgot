package core

import (
	"regexp"
	"sync"
)

// PrecompiledPatterns holds all pre-compiled regex patterns for validators
var PrecompiledPatterns = &PatternCache{}

// PatternCache provides thread-safe access to pre-compiled patterns
type PatternCache struct {
	patterns map[string]*regexp.Regexp
	mu       sync.RWMutex
	once     sync.Once
}

// Init initializes all common regex patterns (called once)
func (pc *PatternCache) Init() {
	pc.once.Do(func() {
		pc.patterns = map[string]*regexp.Regexp{
			// AWS patterns
			"aws_key":              regexp.MustCompile(`^AKIA[0-9A-Z]{16}$`),
			
			// API Key patterns
			"deepseek_key":         regexp.MustCompile(`^sk-[a-f0-9]{32}$`),
			"telegram_bot":         regexp.MustCompile(`^\d+:[A-Za-z0-9_-]{35}$`),
			"discord_token":        regexp.MustCompile(`^[MN][A-Za-z\d]{23}\.[\w-]{6}\.[\w-]{27}$`),
			
			// Stripe patterns
			"stripe_public":        regexp.MustCompile(`^pk_(live|test)_[a-zA-Z0-9]{24,}$`),
			"stripe_restricted":    regexp.MustCompile(`^rk_(live|test)_[a-zA-Z0-9]{24,}$`),
			
			// Generic patterns
			"token_generic":        regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`),
			"hex_60plus":           regexp.MustCompile(`^[A-Fa-f0-9]{60,}$`),
			"ethereum_address":     regexp.MustCompile(`^0x[A-Fa-f0-9]{40}$`),
			"url_http":             regexp.MustCompile(`^https?://[^\s]+$`),
			
			// Private key patterns
			"rsa_private_key":      regexp.MustCompile(`-----BEGIN (?:RSA )?PRIVATE KEY-----`),
			"openssh_private_key":  regexp.MustCompile(`-----BEGIN OPENSSH PRIVATE KEY-----`),
			"pgp_private_key":      regexp.MustCompile(`-----BEGIN PGP PRIVATE KEY BLOCK-----`),
			
			// Connection strings
			"postgres_conn":        regexp.MustCompile(`postgres(?:ql)?://[^\s]+`),
			"mysql_conn":           regexp.MustCompile(`mysql://[^\s]+`),
			"mongodb_conn":         regexp.MustCompile(`mongodb(?:\+srv)?://[^\s]+`),
			
			// JWT
			"jwt_token":            regexp.MustCompile(`^eyJ[A-Za-z0-9-_]+\.eyJ[A-Za-z0-9-_]+\.[A-Za-z0-9-_]+$`),
		}
	})
}

// Match performs a cached regex match
func (pc *PatternCache) Match(patternName string, text string) bool {
	pc.mu.RLock()
	pattern, ok := pc.patterns[patternName]
	pc.mu.RUnlock()
	
	if !ok {
		return false
	}
	
	return pattern.MatchString(text)
}

// Get retrieves a pre-compiled pattern
func (pc *PatternCache) Get(patternName string) *regexp.Regexp {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.patterns[patternName]
}

// Add dynamically adds a new pattern
func (pc *PatternCache) Add(name string, pattern string) error {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	
	pc.mu.Lock()
	pc.patterns[name] = compiled
	pc.mu.Unlock()
	
	return nil
}

// FastMatch is a convenience function for one-off matches
func FastMatch(patternName string, text string) bool {
	// Lazy init on first use
	if len(PrecompiledPatterns.patterns) == 0 {
		PrecompiledPatterns.Init()
	}
	return PrecompiledPatterns.Match(patternName, text)
}
