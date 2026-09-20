package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/trebor048/shhgot/core"
	"gopkg.in/yaml.v3"
)

// Every signature in the shipped config.yaml.example has to be usable. The loader
// in core/signatures.go drops a signature whose regex fails to compile without a
// word of complaint, so a typo or an unsupported construct in the example config
// turns a detection rule into dead weight while the README keeps advertising it.
//
// This test is what makes that visible: it fails loudly, naming each pattern that
// can never fire.
func TestExampleConfigSignaturesAllCompile(t *testing.T) {
	data, err := os.ReadFile("config.yaml.example")
	if err != nil {
		t.Fatalf("read config.yaml.example: %v", err)
	}

	var doc struct {
		Signatures []core.ConfigSignature `yaml:"signatures"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse config.yaml.example: %v", err)
	}
	if len(doc.Signatures) == 0 {
		t.Fatal("config.yaml.example declares no signatures at all")
	}

	var broken []string
	regexCount, matchCount := 0, 0
	for _, sig := range doc.Signatures {
		if sig.Name == "" {
			t.Errorf("a signature has no name: %+v", sig)
			continue
		}
		if sig.Match != "" {
			// A filename/extension/path rule: no regex to compile.
			matchCount++
			continue
		}
		if sig.Regex == "" {
			t.Errorf("signature %q has neither a regex nor a match value, so it can never fire", sig.Name)
			continue
		}
		regexCount++
		if _, err := regexp.Compile(sig.Regex); err != nil {
			broken = append(broken, sig.Name+": "+err.Error())
		}
	}

	if len(broken) > 0 {
		t.Errorf("%d of %d signatures in config.yaml.example cannot compile and are "+
			"therefore silently ignored at runtime (%d regex rules and %d match rules loaded):",
			len(broken), len(doc.Signatures), regexCount, matchCount)
		for _, b := range broken {
			t.Errorf("  - %s", b)
		}
	}

	t.Logf("config.yaml.example: %d signatures, %d regex, %d match, %d unusable",
		len(doc.Signatures), regexCount, matchCount, len(broken))
}
