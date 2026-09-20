package main

import (
	"os"
	"testing"
)

// The mode is decided before flag.Parse() runs, so the argument scanning has to
// understand every form Go's flag package accepts. It used to match only the exact
// strings "--web-port" and "--web-host", which meant "--web-port=9009" and
// "-web-port 9009" were accepted by flag.Parse and then silently dropped: shhgit
// started on 8080 while the operator waited on 9009.
func TestModeFlagsInEveryForm(t *testing.T) {
	cases := []struct {
		name string
		args []string
		mode IntegratedMode
		port string
		host string
	}{
		{"no args", nil, ModeDefault, "8080", "127.0.0.1"},
		{"--web", []string{"--web"}, ModeWeb, "8080", "127.0.0.1"},
		{"-web", []string{"-web"}, ModeWeb, "8080", "127.0.0.1"},
		{"--tui", []string{"--tui"}, ModeTUI, "8080", "127.0.0.1"},
		{"-tui", []string{"-tui"}, ModeTUI, "8080", "127.0.0.1"},
		{"--terminal is the plain terminal mode", []string{"--terminal"}, ModeDefault, "8080", "127.0.0.1"},
		{"--scanner is the plain terminal mode", []string{"--scanner"}, ModeDefault, "8080", "127.0.0.1"},
		{"--web=false does not select web", []string{"--web=false"}, ModeDefault, "8080", "127.0.0.1"},
		{"port, space form", []string{"--web", "--web-port", "9009"}, ModeWeb, "9009", "127.0.0.1"},
		{"port, equals form", []string{"--web", "--web-port=9009"}, ModeWeb, "9009", "127.0.0.1"},
		{"port, single dash", []string{"--web", "-web-port", "9009"}, ModeWeb, "9009", "127.0.0.1"},
		{"port, single dash equals", []string{"--web", "-web-port=9009"}, ModeWeb, "9009", "127.0.0.1"},
		{"host, equals form", []string{"--web", "--web-host=0.0.0.0"}, ModeWeb, "8080", "0.0.0.0"},
		{"host and port, equals forms", []string{"--web", "--web-host=0.0.0.0", "--web-port=9009"}, ModeWeb, "9009", "0.0.0.0"},
		{"port alone does not select web mode", []string{"--web-port=9009"}, ModeDefault, "9009", "127.0.0.1"},
		{"mode flag after a value flag", []string{"--local", "/tmp/x", "--tui"}, ModeTUI, "8080", "127.0.0.1"},
		{"value flag with no value is ignored", []string{"--web", "--web-port"}, ModeWeb, "8080", "127.0.0.1"},
		{"value flag does not eat a following flag", []string{"--web-port", "--tui"}, ModeTUI, "8080", "127.0.0.1"},
		{"value flag does not eat a following core flag", []string{"--web-port", "--local", "/tmp/x"}, ModeDefault, "8080", "127.0.0.1"},
		{"host, space form", []string{"--web", "--web-host", "0.0.0.0"}, ModeWeb, "8080", "0.0.0.0"},
		{"--tui=false does not select tui", []string{"--tui=false"}, ModeDefault, "8080", "127.0.0.1"},
		{"non-flag args are ignored", []string{"scan", "-", "--local=/tmp/x"}, ModeDefault, "8080", "127.0.0.1"},
		{"last mode flag wins", []string{"--web", "--tui"}, ModeTUI, "8080", "127.0.0.1"},
		{"last mode flag wins, web after tui", []string{"--tui", "--web"}, ModeWeb, "8080", "127.0.0.1"},
		{"terminal after web wins", []string{"--web", "--terminal"}, ModeDefault, "8080", "127.0.0.1"},
		{"scanner after web wins", []string{"--web", "--scanner"}, ModeDefault, "8080", "127.0.0.1"},
		{"tui after scanner wins", []string{"--scanner", "--tui"}, ModeTUI, "8080", "127.0.0.1"},
	}

	for _, tc := range cases {
		cfg := &ModeConfig{Mode: ModeDefault, WebPort: "8080", WebHost: "127.0.0.1"}
		args := append([]string{"shhgit"}, tc.args...)
		scanModeArgs(args, cfg)
		if cfg.Mode != tc.mode {
			t.Errorf("%s: mode = %v, want %v", tc.name, cfg.Mode, tc.mode)
		}
		if cfg.WebPort != tc.port {
			t.Errorf("%s: web port = %q, want %q", tc.name, cfg.WebPort, tc.port)
		}
		if cfg.WebHost != tc.host {
			t.Errorf("%s: web host = %q, want %q", tc.name, cfg.WebHost, tc.host)
		}
	}
}

// The mode flags are removed from os.Args before flag.Parse sees them, so removing
// them has to be as form-agnostic as reading them. Leaving "--web-port=9009" behind
// used to be harmless only by accident.
func TestRemoveModeFlagsStripsEveryForm(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()

	os.Args = []string{
		"shhgit",
		"--web",
		"--tui",
		"--web-port", "9009",
		"--web-host=0.0.0.0",
		"--local", "/tmp/x",
		"--csv-path=out.csv",
	}
	mc := &ModeConfig{}
	mc.RemoveModeFlags()

	want := []string{"shhgit", "--local", "/tmp/x", "--csv-path=out.csv"}
	if len(os.Args) != len(want) {
		t.Fatalf("remaining args = %q, want %q", os.Args, want)
	}
	for i := range want {
		if os.Args[i] != want[i] {
			t.Fatalf("remaining args = %q, want %q", os.Args, want)
		}
	}
}

// A value flag with no inline value must not swallow the flag that follows it.
// "--web-port --local /tmp/x" used to drop --local from the arguments, so
// flag.Parse never saw the local scan and the run silently did nothing useful.
func TestRemoveModeFlagsDoesNotEatFollowingFlag(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()

	os.Args = []string{"shhgit", "--web-port", "--local", "/tmp/x"}
	mc := &ModeConfig{}
	mc.RemoveModeFlags()

	want := []string{"shhgit", "--local", "/tmp/x"}
	if len(os.Args) != len(want) {
		t.Fatalf("remaining args = %q, want %q", os.Args, want)
	}
	for i := range want {
		if os.Args[i] != want[i] {
			t.Fatalf("remaining args = %q, want %q", os.Args, want)
		}
	}
}

func TestSplitFlagArg(t *testing.T) {
	cases := []struct {
		in       string
		name     string
		value    string
		hasValue bool
	}{
		{"--web", "web", "", false},
		{"-web", "web", "", false},
		{"--web-port=9009", "web-port", "9009", true},
		{"-web-port=9009", "web-port", "9009", true},
		{"--web=1", "web", "1", true},
		{"/tmp/x", "", "", false},
		{"-", "", "", false},
		{"--", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		name, value, hasValue := splitFlagArg(tc.in)
		if name != tc.name || value != tc.value || hasValue != tc.hasValue {
			t.Errorf("splitFlagArg(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, name, value, hasValue, tc.name, tc.value, tc.hasValue)
		}
	}
}
