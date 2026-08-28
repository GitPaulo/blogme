package index

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// suggesting stands in for the service, recording what was asked of it and answering
// with the completions given.
func suggesting(t *testing.T, terms ...string) (*Index, func() map[string]any, func() string) {
	t.Helper()

	var sent map[string]any
	var path string

	quoted := make([]string, len(terms))
	for i, term := range terms {
		quoted[i] = `{"text": "x", "queryPlusText": ` + mustQuote(term) + `}`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"value": [`+strings.Join(quoted, ",")+`]}`)
	}))
	t.Cleanup(srv.Close)

	return New(srv.URL, "articles", "test-key", ""),
		func() map[string]any { return sent },
		func() string { return path }
}

func mustQuote(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func complete(t *testing.T, idx *Index, q string) []string {
	t.Helper()

	terms, err := idx.Autocomplete(context.Background(), q)
	if err != nil {
		t.Fatalf("autocomplete %q: %v", q, err)
	}
	return terms
}

// The whole query with the last term completed, not the completed term on its own:
// what the caller needs is something it can put in a search box.
func TestAutocompleteReturnsTheWholeCompletedQuery(t *testing.T) {
	idx, _, path := suggesting(t, "kubernetes networking", "kubernetes job")

	terms := complete(t, idx, "kubernet")

	if want := []string{"kubernetes networking", "kubernetes job"}; !slices.Equal(terms, want) {
		t.Errorf("got %q, want %q", terms, want)
	}
	if !strings.HasSuffix(path(), "/docs/autocomplete") {
		t.Errorf("called %q, want the autocomplete endpoint", path())
	}
}

// The endpoint in front of this is anonymous, so every figure that decides what a
// request costs is fixed here rather than taken from a caller. Fuzzy matching is the
// one that matters most: the service documents it as slower and it measured four
// times the latency of an exact completion.
func TestAutocompleteFixesWhatTheRequestCosts(t *testing.T) {
	idx, sent, _ := suggesting(t, "go generics")

	complete(t, idx, "go gen")

	request := sent()
	if request["suggesterName"] != suggesterName {
		t.Errorf("suggesterName = %v, want %q", request["suggesterName"], suggesterName)
	}
	if request["top"] != float64(maxSuggestions) {
		t.Errorf("top = %v, want %d", request["top"], maxSuggestions)
	}
	if request["autocompleteMode"] != "twoTerms" {
		t.Errorf("autocompleteMode = %v, want twoTerms", request["autocompleteMode"])
	}
	if _, ok := request["fuzzy"]; ok {
		t.Error("fuzzy was sent; it costs several times an exact completion and is never wanted")
	}
	if _, ok := request["filter"]; ok {
		t.Error("filter was sent; nothing composes one here, so nothing can inject one")
	}
}

// A completion is third-party text on its way to a search box and an address bar, so
// a title that carries a newline must not put one in either.
func TestAutocompleteCollapsesWhitespaceAndDropsEmpties(t *testing.T) {
	idx, _, _ := suggesting(t, "rust\nasync  runtimes", "   ", "go generics")

	terms := complete(t, idx, "ru")

	if want := []string{"rust async runtimes", "go generics"}; !slices.Equal(terms, want) {
		t.Errorf("got %q, want %q", terms, want)
	}
}

// A completion that arrives after the reader has finished the word is worth nothing
// and still costs billed instance time, so the budget is short and it is enforced.
func TestAutocompleteGivesUpOnASlowService(t *testing.T) {
	suggestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { suggestTimeout = 1500 * time.Millisecond })

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	started := time.Now()
	if _, err := New(srv.URL, "articles", "test-key", "").
		Autocomplete(context.Background(), "kubernet"); err == nil {
		t.Fatal("a service slower than the budget answered anyway")
	}
	if waited := time.Since(started); waited > 150*time.Millisecond {
		t.Errorf("waited %v, want the request abandoned near the 50ms budget", waited)
	}
}
