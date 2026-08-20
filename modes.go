package main

import (
	"fmt"
	"os"
)

// IntegratedMode determines which UI/mode to use
type IntegratedMode int

const (
	ModeDefault IntegratedMode = iota // TUI (default)
	ModeWeb
	ModeTUI
	ModeScanner
)

// ModeConfig holds all mode configuration
type ModeConfig struct {
	Mode    IntegratedMode
	WebPort string
	WebHost string
}

var modeConfig *ModeConfig

// InitIntegratedMode initializes mode selection before main parsing
func InitIntegratedMode() *ModeConfig {
	config := &ModeConfig{
		Mode:    ModeDefault,
		WebPort: "8080",
		WebHost: "127.0.0.1",
	}

	// Check for mode flags early
	for i, arg := range os.Args {
		switch arg {
		case "--web":
			config.Mode = ModeWeb
		case "--tui", "--terminal":
			config.Mode = ModeTUI
		case "--scanner":
			config.Mode = ModeScanner
		case "--web-port":
			if i+1 < len(os.Args) {
				config.WebPort = os.Args[i+1]
			}
		case "--web-host":
			if i+1 < len(os.Args) {
				config.WebHost = os.Args[i+1]
			}
		}
	}

	modeConfig = config
	return config
}

// RemoveModeFlags removes mode flags from os.Args for flag parsing
func (mc *ModeConfig) RemoveModeFlags() {
	newArgs := []string{os.Args[0]}
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch arg {
		case "--web", "--tui", "--terminal", "--scanner":
			continue
		case "--web-port", "--web-host":
			i++ // Skip next arg (value)
			continue
		default:
			newArgs = append(newArgs, arg)
		}
	}
	os.Args = newArgs
}

// SetupIntegratedUI sets up the integrated UI based on mode
func (mc *ModeConfig) SetupIntegratedUI() error {
	switch mc.Mode {
	case ModeWeb:
		return StartWebServer(mc.WebHost, mc.WebPort)
	case ModeTUI, ModeDefault:
		return StartTUI()
	}
	return nil
}

// PrintModeInfo prints information about the selected mode
func (mc *ModeConfig) PrintModeInfo() {
	switch mc.Mode {
	case ModeWeb:
		fmt.Printf("🌐 Mode: Web Dashboard\n")
		fmt.Printf("   Address: http://%s:%s\n", mc.WebHost, mc.WebPort)
	case ModeTUI, ModeDefault:
		fmt.Printf("🖥️  Mode: Terminal UI (TUI)\n")
	case ModeScanner:
		fmt.Printf("🔍 Mode: Scanner Only\n")
	}
}
