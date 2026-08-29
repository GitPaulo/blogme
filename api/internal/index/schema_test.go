package index

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// The parts of infra/search-index.json this package depends on. Only these: the schema
// carries filterable and facetable flags and a semantic configuration too, and a test
// that restated all of it would fail on every edit rather than on the ones that matter.
type schema struct {
	Fields []struct {
		Name       string `json:"name"`
		Searchable bool   `json:"searchable"`
		Analyzer   string `json:"analyzer"`
	} `json:"fields"`
	ScoringProfiles []struct {
		Name string `json:"name"`
		Text struct {
			Weights map[string]int `json:"weights"`
		} `json:"text"`
	} `json:"scoringProfiles"`
}

func readSchema(t *testing.T) schema {
	t.Helper()

	raw, err := os.ReadFile("../../../infra/search-index.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var s schema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	if len(s.Fields) == 0 {
		t.Fatal("schema declares no fields")
	}
	return s
}

// The bug this exists to prevent, stated as a rule.
//
// author and topics were created searchable with no analyzer, which the service reads
// as standard.lucene — the one analyzer that keeps English stopwords. Every other
// searchable field discards them, so "a tour of go" reached those two fields as four
// terms and the rest as two. Under searchMode "all" that meant "a" and "of" had to
// match somewhere, and only a byline could oblige: the query returned 24 documents,
// every one of them somebody whose name contains those words, in place of the 11,508
// that are actually about Go.
//
// It went unnoticed because nothing failed. The schema was valid, the query was valid,
// and the answer was wrong.
//
// Those two fields are still there and still have no analyzer, because the service
// will not add one — "Existing field 'author' cannot be changed." — and rebuilding the
// index to fix a field would empty every quality score with it. They are harmless now
// only because nothing is matched against them: author has the analysed copy
// authorText in its place, and topics is out of searchFields altogether.
//
// So they are named here rather than exempted by a rule. The list is closed: a third
// field arriving without an analyzer is either a mistake or a decision worth writing
// down, and both are worth a failing test.
func TestEverySearchableFieldDeclaresAnAnalyzer(t *testing.T) {
	// Created before the analyzer mattered and unchangeable since. Neither may appear
	// in searchFields, which the loop below is what enforces.
	grandfathered := []string{"author", "topics"}

	for _, f := range readSchema(t).Fields {
		if !f.Searchable || f.Analyzer != "" {
			continue
		}
		if slices.Contains(grandfathered, f.Name) {
			if slices.Contains(strings.Split(searchFields, ","), f.Name) {
				t.Errorf("searchFields names %q, which is searchable with no analyzer: it "+
					"keeps the stopwords every other field drops, and under searchMode "+
					"\"all\" that makes them terms only a byline can match", f.Name)
			}
			continue
		}
		t.Errorf("field %q is searchable but declares no analyzer, so it falls back to "+
			"standard.lucene and keeps the stopwords the rest of the index drops; give it "+
			"one, or leave it out of searchFields and say here why it is unfixable", f.Name)
	}
}

// searchFields is a string in a request body, so nothing but this notices when it names
// a field the schema does not have, or stops naming one it should.
func TestSearchFieldsAreSearchableAndAnalysedAlike(t *testing.T) {
	s := readSchema(t)

	declared := make(map[string]string, len(s.Fields))
	searchable := make(map[string]bool, len(s.Fields))
	for _, f := range s.Fields {
		declared[f.Name] = f.Analyzer
		searchable[f.Name] = f.Searchable
	}

	var analyzers []string
	for _, name := range strings.Split(searchFields, ",") {
		analyzer, ok := declared[name]
		switch {
		case !ok:
			t.Errorf("searchFields names %q, which the schema does not declare", name)
			continue
		case !searchable[name]:
			t.Errorf("searchFields names %q, which is not searchable", name)
			continue
		}
		// A field with no analyzer named is not a field with no analyzer: the service
		// gives it standard.lucene. Saying so is the difference between a readable
		// failure and one that reports an empty string.
		if analyzer == "" {
			analyzer = "standard.lucene (undeclared)"
		}
		if !slices.Contains(analyzers, analyzer) {
			analyzers = append(analyzers, analyzer)
		}
	}

	// One analyzer across the set, because searchMode "all" makes the strictest field
	// the query. A field that keeps a term the others drop turns that term into a
	// requirement only it can satisfy.
	if len(analyzers) > 1 {
		t.Errorf("searchFields mixes analyzers %v; every field a query is matched "+
			"against has to analyse it the same way", analyzers)
	}
}

// A weight on a field nothing is matched against does nothing at all. That is worth
// failing over rather than leaving in place: the profiles are the file anybody tuning
// ranking reads first, and a line that has no effect is a wrong answer to "what is
// this ranked by".
func TestScoringProfilesWeightOnlyTheFieldsSearched(t *testing.T) {
	matched := strings.Split(searchFields, ",")

	for _, p := range readSchema(t).ScoringProfiles {
		for field := range p.Text.Weights {
			if !slices.Contains(matched, field) {
				t.Errorf("profile %q weights %q, which is not in searchFields, so the "+
					"weight is never applied", p.Name, field)
			}
		}
	}
}
