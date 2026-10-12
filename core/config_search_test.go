package core

import (
	"reflect"
	"testing"
)

func TestSearchConfigDefaults(t *testing.T) {
	c := &Config{}
	if c.SearchEnabled() {
		t.Fatal("search must be disabled by default")
	}
	queries, perPage, maxPages, maxResults, sleepMS, minStars := c.SearchValues()
	if len(queries) != 0 || perPage != 100 || maxPages != 1 || maxResults != 1000 || sleepMS != 2000 || minStars != 0 {
		t.Fatalf("defaults = queries %v perPage %d maxPages %d maxResults %d sleepMS %d minStars %d",
			queries, perPage, maxPages, maxResults, sleepMS, minStars)
	}
}

func TestSearchConfigValues(t *testing.T) {
	enabled := true
	perPage, maxPages, maxResults, sleepMS, minStars := 5, 4, 500, 250, 3
	c := &Config{}
	c.Search = SearchConfig{
		Enabled:    &enabled,
		Queries:    []string{"one", "two"},
		PerPage:    &perPage,
		MaxPages:   &maxPages,
		MaxResults: &maxResults,
		SleepMS:    &sleepMS,
		MinStars:   &minStars,
	}
	if !c.SearchEnabled() {
		t.Fatal("search.enabled: true must report enabled")
	}
	queries, pp, mp, mr, sm, ms := c.SearchValues()
	if !reflect.DeepEqual(queries, []string{"one", "two"}) || pp != 5 || mp != 4 || mr != 500 || sm != 250 || ms != 3 {
		t.Fatalf("values = queries %v perPage %d maxPages %d maxResults %d sleepMS %d minStars %d",
			queries, pp, mp, mr, sm, ms)
	}
}

// SignatureAnchors must contribute only contents patterns' mandatory literals,
// and BuildDorks must drop the generic/short ones. Filename and path rules
// contribute nothing.
func TestSignatureAnchorsBuildDorks(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{
		{Name: "AWS Access Key", Part: PartContents, Regex: `AKIA[0-9A-Z]{16}`},
		{Name: "OpenAI", Part: PartContents, Regex: `sk-[A-Za-z0-9]{20,}`},
		{Name: "Env File", Part: PartFilename, Match: ".env"},
		{Name: "Generic", Part: PartContents, Regex: `(?i)api_key\s*=\s*\S+`},
	}
	sigs := GetSignatures(session)
	got := BuildDorks(SignatureAnchors(sigs))
	want := []string{`"AKIA" in:file NOT test NOT example NOT sample NOT mock`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildDorks(SignatureAnchors) = %#v, want %#v", got, want)
	}
}
