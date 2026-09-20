package main

import (
	"fmt"
	"os"
	"strings"
)

// IntegratedMode determines which UI/mode to use.
type IntegratedMode int

const (
	// ModeDefault is the plain terminal mode: the scanner runs in the foreground and
	// every finding is printed as it is found. It is what you get with no flags.
	ModeDefault IntegratedMode = iota
	// ModeWeb runs the embedded dashboard and the scanner together.
	ModeWeb
	// ModeTUI runs the interactive full-screen terminal UI.
	ModeTUI
	// ModeScanner is an explicit alias for ModeDefault, kept because it has always
	// been documented; it behaves exactly like the default.
	ModeScanner
)

// ModeConfig holds all mode configuration.
type ModeConfig struct {
	Mode    IntegratedMode
	WebPort string
	WebHost string
}

var modeConfig *ModeConfig

// splitFlagArg normalises a command-line argument into its flag name and inline
// value, covering every form Go's flag package accepts: -web, --web, -web=true,
// --web-port=9009. Args that are not flags, and the bare "--" terminator, return an
// empty name.
func splitFlagArg(arg string) (name, value string, hasValue bool) {
	if len(arg) < 2 || arg[0] != '-' {
		return "", "", false
	}
	trimmed := arg[1:]
	if strings.HasPrefix(trimmed, "-") {
		trimmed = trimmed[1:]
	}
	if trimmed == "" || trimmed == "-" {
		return "", "", false
	}
	if i := strings.IndexByte(trimmed, '='); i >= 0 {
		return trimmed[:i], trimmed[i+1:], true
	}
	return trimmed, "", false
}

// flagWantsTrue reports whether a boolean flag was given in a form that means true.
// "-web" means true; "-web=false" means false.
func flagWantsTrue(value string, hasValue bool) bool {
	if !hasValue {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// InitIntegratedMode initializes mode selection before main parsing.
//
// The mode has to be known before flag.Parse() runs, because the mode banner is
// printed before the session (and therefore the flags) is initialized. That is why
// this reads the arguments itself - but it must understand the same syntax the flag
// package does. It used to match only the exact strings "--web-port" and
// "--web-host", so "--web-port=9009" and "-web-port 9009" were accepted by the flag
// package and then silently ignored: the server bound the default 8080 instead.
func InitIntegratedMode() *ModeConfig {
	config := &ModeConfig{
		Mode:    ModeDefault,
		WebPort: "8080",
		WebHost: "127.0.0.1",
	}
	scanModeArgs(os.Args, config)
	modeConfig = config
	return config
}

// takesValue reports whether arg can be consumed as the value of a preceding
// value flag (--web-port, --web-host). A value must not itself look like a flag:
// otherwise "--web-port --tui" would bind "--tui" as the port and silently drop
// the mode selection, and "--web-port --local /tmp" would swallow --local.
func takesValue(arg string) bool {
	if arg == "--" || arg == "-" {
		return false
	}
	name, _, _ := splitFlagArg(arg)
	return name == ""
}

// scanModeArgs applies the mode flags found in args to cfg.
func scanModeArgs(args []string, cfg *ModeConfig) {
	for i := 0; i < len(args); i++ {
		name, value, hasValue := splitFlagArg(args[i])
		switch name {
		case "":
			continue
		case "web":
			if flagWantsTrue(value, hasValue) {
				cfg.Mode = ModeWeb
			}
		case "tui":
			if flagWantsTrue(value, hasValue) {
				cfg.Mode = ModeTUI
			}
		case "terminal", "scanner":
			// Plain terminal output, i.e. the default mode.
			if flagWantsTrue(value, hasValue) {
				cfg.Mode = ModeDefault
			}
		case "web-port", "web-host":
			if hasValue {
				if name == "web-port" {
					cfg.WebPort = value
				} else {
					cfg.WebHost = value
				}
				continue
			}
			if i+1 < len(args) && takesValue(args[i+1]) {
				i++
				if name == "web-port" {
					cfg.WebPort = args[i]
				} else {
					cfg.WebHost = args[i]
				}
			}
		}
	}
}

// RemoveModeFlags removes mode flags from os.Args for flag parsing, in every form
// they can be written.
func (mc *ModeConfig) RemoveModeFlags() {
	newArgs := []string{os.Args[0]}
	for i := 1; i < len(os.Args); i++ {
		name, _, hasValue := splitFlagArg(os.Args[i])
		switch name {
		case "web", "tui", "terminal", "scanner":
			continue
		case "web-port", "web-host":
			if !hasValue && i+1 < len(os.Args) && takesValue(os.Args[i+1]) {
				i++ // Skip next arg (value)
			}
			continue
		}
		newArgs = append(newArgs, os.Args[i])
	}
	os.Args = newArgs
}

// SetupIntegratedUI starts the blocking UI for the selected mode. Only the web mode
// goes through here: the terminal and TUI modes start their UI from main, around the
// scanner, so that a scanner running in a goroutine feeds it.
func (mc *ModeConfig) SetupIntegratedUI() error {
	switch mc.Mode {
	case ModeWeb:
		return StartWebServer(mc.WebHost, mc.WebPort)
	}
	return nil
}

// PrintModeInfo prints information about the selected mode.
func (mc *ModeConfig) PrintModeInfo() {
	switch mc.Mode {
	case ModeWeb:
		fmt.Printf("🌐 Mode: Web Dashboard\n")
		fmt.Printf("   Address: http://%s:%s\n", mc.WebHost, mc.WebPort)
	case ModeTUI:
		fmt.Printf("🖥️  Mode: Terminal UI (interactive) - press ? for keys, q to quit\n")
	case ModeScanner:
		fmt.Printf("🔍 Mode: Scanner Only (plain terminal output)\n")
	default:
		fmt.Printf("📟 Mode: Terminal (live match feed)\n")
	}
}
