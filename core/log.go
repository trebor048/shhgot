package core

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fatih/color"
)

const (
	FATAL     = 5
	ERROR     = 4
	IMPORTANT = 3
	WARN      = 2
	INFO      = 1
	DEBUG     = 0
)

var LogColors = map[int]*color.Color{
	FATAL:     color.New(color.FgRed).Add(color.Bold),
	ERROR:     color.New(color.FgRed),
	WARN:      color.New(color.FgYellow),
	IMPORTANT: color.New(),
	DEBUG:     color.New(color.Faint),
}

// HexToRGB converts hex color to RGB values
func HexToRGB(hex string) (uint8, uint8, uint8) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 255, 255, 255
	}
	var r, g, b uint8
	fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

// GetColorFromHex creates a color.Color from hex string
func GetColorFromHex(hex string) *color.Color {
	// Use a simple approach - map common colors to ANSI codes
	// For full RGB support, would need a newer version of fatih/color
	hex = strings.ToLower(hex)
	
	switch hex {
	case "#10a37f": // OpenAI green
		return color.New(color.FgGreen).Add(color.Bold)
	case "#000000": // Black
		return color.New(color.FgBlack)
	case "#ff0000": // Red
		return color.New(color.FgRed).Add(color.Bold)
	case "#ff6600": // Orange
		return color.New(color.FgYellow).Add(color.Bold)
	case "#0066cc": // Blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#1d99f3": // Light blue
		return color.New(color.FgCyan).Add(color.Bold)
	case "#4a90e2": // Medium blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#e01e5a": // Slack pink
		return color.New(color.FgMagenta).Add(color.Bold)
	case "#5865f2": // Discord blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#0088cc": // Telegram blue
		return color.New(color.FgCyan).Add(color.Bold)
	case "#1877f2": // Facebook blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#635bff": // Stripe purple
		return color.New(color.FgMagenta).Add(color.Bold)
	case "#003087": // PayPal blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#ff9900": // Amazon orange
		return color.New(color.FgYellow).Add(color.Bold)
	case "#f22f46": // Twilio red
		return color.New(color.FgRed).Add(color.Bold)
	case "#1a73e8": // SendGrid blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#f06b66": // MailGun red
		return color.New(color.FgRed).Add(color.Bold)
	case "#ffe01b": // MailChimp yellow
		return color.New(color.FgYellow).Add(color.Bold)
	case "#cc0000": // Rails red
		return color.New(color.FgRed).Add(color.Bold)
	case "#307146": // Spotify green
		return color.New(color.FgGreen).Add(color.Bold)
	case "#f04e86": // Tidal pink
		return color.New(color.FgMagenta).Add(color.Bold)
	case "#00758f": // MySQL blue
		return color.New(color.FgCyan).Add(color.Bold)
	case "#336791": // PostgreSQL blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#13aa52": // MongoDB green
		return color.New(color.FgGreen).Add(color.Bold)
	case "#092e20": // Django green
		return color.New(color.FgGreen).Add(color.Bold)
	case "#2496ed": // Docker blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#f1502f": // Git orange
		return color.New(color.FgYellow).Add(color.Bold)
	case "#cb3837": // NPM red
		return color.New(color.FgRed).Add(color.Bold)
	case "#430098": // Heroku purple
		return color.New(color.FgMagenta).Add(color.Bold)
	case "#0080ff": // DigitalOcean blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#ffc900": // Chef yellow
		return color.New(color.FgYellow).Add(color.Bold)
	case "#ecd53f": // Env yellow
		return color.New(color.FgYellow).Add(color.Bold)
	case "#4285f4": // Google blue
		return color.New(color.FgBlue).Add(color.Bold)
	case "#ff7139": // Firefox orange
		return color.New(color.FgYellow).Add(color.Bold)
	case "#555555": // Apple gray
		return color.New(color.FgWhite)
	case "#f7931a": // Bitcoin orange
		return color.New(color.FgYellow).Add(color.Bold)
	case "#eea53a": // Crypto yellow
		return color.New(color.FgYellow).Add(color.Bold)
	default:
		// Default to white for unknown colors
		return color.New(color.FgWhite)
	}
}

type Logger struct {
	sync.Mutex

	debug       bool
	silent      bool
	config      *Config
	RateLimited atomic.Int64
	LogWriter   func(string) // Optional: redirect log output (e.g., to TUI)
}

func (l *Logger) SetDebug(d bool) {
	l.debug = d
}

func (l *Logger) SetSilent(d bool) {
	l.silent = d
}

func (l *Logger) SetConfig(c *Config) {
	l.config = c
}

func (l *Logger) Log(level int, format string, args ...interface{}) {
	l.Lock()
	defer l.Unlock()

	if level == DEBUG && !l.debug {
		return
	}

	if l.silent && level < IMPORTANT {
		return
	}

	if c, ok := LogColors[level]; ok {
		line := c.Sprintf("\r\033[K"+format+"\n", args...)
		if l.LogWriter != nil {
			l.LogWriter(line)
		} else {
			fmt.Print(line)
		}
	} else {
		line := fmt.Sprintf("\r\033[K"+format+"\n", args...)
		if l.LogWriter != nil {
			l.LogWriter(line)
		} else {
			fmt.Print(line)
		}
	}

	if level > WARN && session.Config.Webhook != "" {
		text := colorStrip(fmt.Sprintf(format, args...))
		payload := fmt.Sprintf(session.Config.WebhookPayload, text)
		http.Post(session.Config.Webhook, "application/json", strings.NewReader(payload))
	}

	if level == FATAL {
		os.Exit(1)
	}
}

// LogWithColor logs a message with a custom hex color
func (l *Logger) LogWithColor(hexColor string, format string, args ...interface{}) {
	l.Lock()
	defer l.Unlock()

	c := GetColorFromHex(hexColor)
	line := c.Sprintf("\r\033[K"+format+"\n", args...)
	if l.LogWriter != nil {
		l.LogWriter(line)
	} else {
		fmt.Print(line)
	}
}

// RecordRateLimit increments the rate-limited token counter (for progress display).
func (l *Logger) RecordRateLimit() {
	l.RateLimited.Add(1)
}

func (l *Logger) Fatal(format string, args ...interface{}) {
	l.Log(FATAL, format, args...)
}

func (l *Logger) Error(format string, args ...interface{}) {
	l.Log(ERROR, format, args...)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	l.Log(WARN, format, args...)
}

func (l *Logger) Important(format string, args ...interface{}) {
	l.Log(IMPORTANT, format, args...)
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.Log(INFO, format, args...)
}

func (l *Logger) Debug(format string, args ...interface{}) {
	l.Log(DEBUG, format, args...)
}

func colorStrip(str string) string {
	ansi := "[\u001B\u009B][[\\]()#;?]*(?:(?:(?:[a-zA-Z\\d]*(?:;[a-zA-Z\\d]*)*)?\u0007)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PRZcf-ntqry=><~]))"
	re := regexp.MustCompile(ansi)
	return re.ReplaceAllString(str, "")
}
