package core

import (
	"regexp"
	"strings"
)

// SemanticAnalyzer performs context-aware analysis of code to detect credentials
type SemanticAnalyzer struct {
	log *Logger
}

// NewSemanticAnalyzer creates a new semantic analyzer
func NewSemanticAnalyzer(log *Logger) *SemanticAnalyzer {
	return &SemanticAnalyzer{
		log: log,
	}
}

// CredentialPattern represents a detected credential pattern
type CredentialPattern struct {
	Type        string   // password, api_key, private_key, etc.
	Name        string   // variable name
	Value       string   // the value
	Context     string   // surrounding code context
	Confidence  int      // 0-100
	LineNumber  int
	ColumnStart int
	ColumnEnd   int
}

// AnalyzeCode performs semantic analysis on code content
func (sa *SemanticAnalyzer) AnalyzeCode(content string, filename string) []CredentialPattern {
	var patterns []CredentialPattern

	// Analyze based on file type
	switch {
	case strings.HasSuffix(filename, ".py"):
		patterns = append(patterns, sa.analyzePython(content)...)
	case strings.HasSuffix(filename, ".js") || strings.HasSuffix(filename, ".ts"):
		patterns = append(patterns, sa.analyzeJavaScript(content)...)
	case strings.HasSuffix(filename, ".go"):
		patterns = append(patterns, sa.analyzeGo(content)...)
	case strings.HasSuffix(filename, ".java"):
		patterns = append(patterns, sa.analyzeJava(content)...)
	case strings.HasSuffix(filename, ".cs"):
		patterns = append(patterns, sa.analyzeCSharp(content)...)
	case strings.HasSuffix(filename, ".rb"):
		patterns = append(patterns, sa.analyzeRuby(content)...)
	case strings.HasSuffix(filename, ".php"):
		patterns = append(patterns, sa.analyzePHP(content)...)
	case strings.HasSuffix(filename, ".rs"):
		patterns = append(patterns, sa.analyzeRust(content)...)
	case strings.HasSuffix(filename, ".cpp") || strings.HasSuffix(filename, ".c"):
		patterns = append(patterns, sa.analyzeC(content)...)
	case strings.HasSuffix(filename, ".kt"):
		patterns = append(patterns, sa.analyzeKotlin(content)...)
	}

	// Generic analysis for all files
	patterns = append(patterns, sa.analyzeGeneric(content)...)

	return patterns
}

// analyzePython analyzes Python code for credentials
func (sa *SemanticAnalyzer) analyzePython(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: variable = "value"
	re := regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			varName := content[match[2]:match[3]]
			value := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: os.environ["KEY"] = "value"
	re = regexp.MustCompile(`os\.environ\[["\']([A-Z_]+)["\']\]\s*=\s*["\']([^\'"]+)["\']`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			varName := content[match[2]:match[3]]
			value := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 80,
			})
		}
	}

	// Pattern: requests.post(url, auth=(user, pass))
	re = regexp.MustCompile(`auth\s*=\s*\(["\']([^\'"]+)["\'],\s*["\']([^\'"]+)["\']\)`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			username := content[match[2]:match[3]]
			password := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       "username_password",
				Name:       "auth",
				Value:      username + ":" + password,
				Confidence: 85,
			})
		}
	}

	return patterns
}

// analyzeJavaScript analyzes JavaScript/TypeScript code for credentials
func (sa *SemanticAnalyzer) analyzeJavaScript(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: const/let/var name = "value"
	re := regexp.MustCompile(`(?i)(const|let|var)\s+(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 8 {
			varName := content[match[4]:match[5]]
			value := content[match[6]:match[7]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: process.env.KEY = "value"
	re = regexp.MustCompile(`process\.env\.([A-Z_]+)\s*=\s*["\']([^\'"]+)["\']`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			varName := content[match[2]:match[3]]
			value := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 80,
			})
		}
	}

	// Pattern: axios.defaults.headers.common['Authorization'] = "Bearer token"
	re = regexp.MustCompile(`Authorization["\']?\s*:\s*["\']Bearer\s+([^\'"]+)["\']`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 4 {
			token := content[match[2]:match[3]]

			patterns = append(patterns, CredentialPattern{
				Type:       "bearer_token",
				Name:       "Authorization",
				Value:      token,
				Confidence: 85,
			})
		}
	}

	return patterns
}

// analyzeGo analyzes Go code for credentials
func (sa *SemanticAnalyzer) analyzeGo(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: var name = "value"
	re := regexp.MustCompile(`(?i)(var|const)\s+(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 8 {
			varName := content[match[4]:match[5]]
			value := content[match[6]:match[7]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: os.Getenv("KEY")
	re = regexp.MustCompile(`os\.Getenv\(["\']([A-Z_]+)["\']\)`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 4 {
			varName := content[match[2]:match[3]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      "[environment variable]",
				Confidence: 60,
			})
		}
	}

	return patterns
}

// analyzeJava analyzes Java code for credentials
func (sa *SemanticAnalyzer) analyzeJava(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: String name = "value"
	re := regexp.MustCompile(`(?i)String\s+(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			varName := content[match[2]:match[3]]
			value := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: System.getenv("KEY")
	re = regexp.MustCompile(`System\.getenv\(["\']([A-Z_]+)["\']\)`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 4 {
			varName := content[match[2]:match[3]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      "[environment variable]",
				Confidence: 60,
			})
		}
	}

	return patterns
}

// analyzeCSharp analyzes C# code for credentials
func (sa *SemanticAnalyzer) analyzeCSharp(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: string name = "value"
	re := regexp.MustCompile(`(?i)string\s+(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			varName := content[match[2]:match[3]]
			value := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: Environment.GetEnvironmentVariable("KEY")
	re = regexp.MustCompile(`Environment\.GetEnvironmentVariable\(["\']([A-Z_]+)["\']\)`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 4 {
			varName := content[match[2]:match[3]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      "[environment variable]",
				Confidence: 60,
			})
		}
	}

	return patterns
}

// analyzeRuby analyzes Ruby code for credentials
func (sa *SemanticAnalyzer) analyzeRuby(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: name = "value"
	re := regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			varName := content[match[2]:match[3]]
			value := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: ENV["KEY"]
	re = regexp.MustCompile(`ENV\[["\']([A-Z_]+)["\']\]`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 4 {
			varName := content[match[2]:match[3]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      "[environment variable]",
				Confidence: 60,
			})
		}
	}

	return patterns
}

// analyzePHP analyzes PHP code for credentials
func (sa *SemanticAnalyzer) analyzePHP(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: $name = "value"
	re := regexp.MustCompile(`(?i)\$(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			varName := content[match[2]:match[3]]
			value := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: $_ENV["KEY"]
	re = regexp.MustCompile(`\$_ENV\[["\']([A-Z_]+)["\']\]`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 4 {
			varName := content[match[2]:match[3]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      "[environment variable]",
				Confidence: 60,
			})
		}
	}

	return patterns
}

// analyzeRust analyzes Rust code for credentials
func (sa *SemanticAnalyzer) analyzeRust(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: let name = "value"
	re := regexp.MustCompile(`(?i)let\s+(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			varName := content[match[2]:match[3]]
			value := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: env::var("KEY")
	re = regexp.MustCompile(`env::var\(["\']([A-Z_]+)["\']\)`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 4 {
			varName := content[match[2]:match[3]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      "[environment variable]",
				Confidence: 60,
			})
		}
	}

	return patterns
}

// analyzeC analyzes C/C++ code for credentials
func (sa *SemanticAnalyzer) analyzeC(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: char* name = "value"
	re := regexp.MustCompile(`(?i)(char\*|const char\*)\s+(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 8 {
			varName := content[match[4]:match[5]]
			value := content[match[6]:match[7]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: getenv("KEY")
	re = regexp.MustCompile(`getenv\(["\']([A-Z_]+)["\']\)`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 4 {
			varName := content[match[2]:match[3]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      "[environment variable]",
				Confidence: 60,
			})
		}
	}

	return patterns
}

// analyzeKotlin analyzes Kotlin code for credentials
func (sa *SemanticAnalyzer) analyzeKotlin(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: val/var name = "value"
	re := regexp.MustCompile(`(?i)(val|var)\s+(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*=\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 8 {
			varName := content[match[4]:match[5]]
			value := content[match[6]:match[7]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 75,
			})
		}
	}

	// Pattern: System.getenv("KEY")
	re = regexp.MustCompile(`System\.getenv\(["\']([A-Z_]+)["\']\)`)
	matches = re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 4 {
			varName := content[match[2]:match[3]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      "[environment variable]",
				Confidence: 60,
			})
		}
	}

	return patterns
}

// analyzeGeneric performs generic analysis on any code
func (sa *SemanticAnalyzer) analyzeGeneric(content string) []CredentialPattern {
	var patterns []CredentialPattern

	// Pattern: key: value (YAML/JSON style)
	re := regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api_key|apikey|auth|credential|key|private_key|privatekey|access_key|accesskey)\s*[:=]\s*["\']([^\'"]+)["\']`)
	matches := re.FindAllStringSubmatchIndex(content, -1)

	for _, match := range matches {
		if len(match) >= 6 {
			varName := content[match[2]:match[3]]
			value := content[match[4]:match[5]]

			patterns = append(patterns, CredentialPattern{
				Type:       sa.classifyCredentialType(varName),
				Name:       varName,
				Value:      value,
				Confidence: 70,
			})
		}
	}

	return patterns
}

// classifyCredentialType classifies the type of credential based on variable name
func (sa *SemanticAnalyzer) classifyCredentialType(varName string) string {
	varLower := strings.ToLower(varName)

	switch {
	case strings.Contains(varLower, "password") || strings.Contains(varLower, "passwd") || strings.Contains(varLower, "pwd"):
		return "password"
	case strings.Contains(varLower, "api_key") || strings.Contains(varLower, "apikey"):
		return "api_key"
	case strings.Contains(varLower, "token"):
		return "token"
	case strings.Contains(varLower, "secret"):
		return "secret"
	case strings.Contains(varLower, "private_key") || strings.Contains(varLower, "privatekey"):
		return "private_key"
	case strings.Contains(varLower, "access_key") || strings.Contains(varLower, "accesskey"):
		return "access_key"
	case strings.Contains(varLower, "auth"):
		return "auth"
	case strings.Contains(varLower, "credential"):
		return "credential"
	case strings.Contains(varLower, "key"):
		return "key"
	default:
		return "unknown"
	}
}
