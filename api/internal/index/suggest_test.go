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

	terms, err := idx.autocomplete(context.Background(), q)
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
	// More than are returned, because half of what the service offers is dropped below.
	// Fixed all the same: the figure is this package's, not a caller's.
	if want := float64(maxSuggestions * suggestOverFetch); request["top"] != want {
		t.Errorf("top = %v, want %v", request["top"], want)
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
		autocomplete(context.Background(), "kubernet"); err == nil {
		t.Fatal("a service slower than the budget answered anyway")
	}
	if waited := time.Since(started); waited > 150*time.Millisecond {
		t.Errorf("waited %v, want the request abandoned near the 50ms budget", waited)
	}
}

// The suggester extends a query by one term and has no notion of which terms are worth
// extending it by, so it offers "rust and" beside "rust compiler". Half of what it
// returned for the eight rows a reader saw was of the first kind.
func TestAutocompleteDropsCompletionsThatSayNothing(t *testing.T) {
	idx, _, _ := suggesting(t,
		"rust and", "rust for", "rust 1", "rust in", "rust 2026",
		"rust compiler", "rust async", "rust crate")

	terms := complete(t, idx, "rust")

	want := []string{"rust compiler", "rust async", "rust crate"}
	if !slices.Equal(terms, want) {
		t.Errorf("got %q, want %q", terms, want)
	}
}

// Only the added words are judged. A reader part-way through a phrase of function words
// is completing their own query, and the list exists to say what could follow it.
func TestAutocompleteJudgesOnlyWhatACompletionAdds(t *testing.T) {
	idx, _, _ := suggesting(t, "how to build", "how to a", "how to 2026", "how to debug")

	terms := complete(t, idx, "how to")

	if want := []string{"how to build", "how to debug"}; !slices.Equal(terms, want) {
		t.Errorf("got %q, want %q", terms, want)
	}
}

// A version number is a subject; a bare year or list number is not.
func TestAutocompleteKeepsVersionsAndDropsBareNumbers(t *testing.T) {
	idx, _, _ := suggesting(t, "python 3", "python 3.14", "python 2026", "python 2.7")

	terms := complete(t, idx, "python")

	if want := []string{"python 3.14", "python 2.7"}; !slices.Equal(terms, want) {
		t.Errorf("got %q, want %q", terms, want)
	}
}

// A completion that puts back what is already in the box spends one of eight rows to
// say nothing, and the same phrase reached through two titles is still one phrase.
func TestAutocompleteDropsEchoesAndRepeats(t *testing.T) {
	idx, _, _ := suggesting(t, "rust", "Rust Compiler", "rust compiler", "rust async")

	terms := complete(t, idx, "rust")

	if want := []string{"Rust Compiler", "rust async"}; !slices.Equal(terms, want) {
		t.Errorf("got %q, want %q", terms, want)
	}
}

// A plural is the same suggestion as its singular, and a list of eight that offers both
// has spent two rows on one idea. Short words that merely end in "s" are left alone.
func TestAutocompleteCollapsesPlurals(t *testing.T) {
	idx, _, _ := suggesting(t,
		"docker image", "docker images", "docker container", "docker containers",
		"docker aws", "docker aw")

	terms := complete(t, idx, "docker")

	want := []string{"docker image", "docker container", "docker aws", "docker aw"}
	if !slices.Equal(terms, want) {
		t.Errorf("got %q, want %q", terms, want)
	}
}

// suggestingBoth stands in for a service answering both halves of a suggestion: titles
// from /docs/suggest, completions from /docs/autocomplete.
func suggestingBoth(t *testing.T, titles, completions []string) *Index {
	t.Helper()

	title := make([]string, len(titles))
	for i, text := range titles {
		title[i] = `{"@search.text": ` + mustQuote(text) + `}`
	}
	completion := make([]string, len(completions))
	for i, text := range completions {
		completion[i] = `{"text": "x", "queryPlusText": ` + mustQuote(text) + `}`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := completion
		if strings.HasSuffix(r.URL.Path, "/docs/suggest") {
			rows = title
		}
		_, _ = io.WriteString(w, `{"value": [`+strings.Join(rows, ",")+`]}`)
	}))
	t.Cleanup(srv.Close)

	return New(srv.URL, "articles", "test-key", "")
}

func suggested(t *testing.T, idx *Index, q string) []Suggestion {
	t.Helper()

	found, err := idx.Suggest(context.Background(), q)
	if err != nil {
		t.Fatalf("suggest %q: %v", q, err)
	}
	return found
}

// Titles first because they are the ranked ones, capped so that completions still get
// rows, and the whole list held to eight.
func TestSuggestPutsRankedTitlesFirstAndCapsThem(t *testing.T) {
	idx := suggestingBoth(t,
		[]string{"Go Concurrency Patterns", "Notes on Go concurrency", "Go Concurrency Starter Pack", "A fourth title"},
		[]string{"go concept art", "go concatenate two", "go concentration camps",
			"go conceptual art", "go concept based", "go concrete types"})

	found := suggested(t, idx, "go conc")

	if len(found) != maxSuggestions {
		t.Fatalf("got %d rows, want %d", len(found), maxSuggestions)
	}
	for i, row := range found[:maxTitles] {
		if row.Kind != KindTitle {
			t.Errorf("row %d is %q, want a title", i, row.Kind)
		}
	}
	if found[0].Text != "Go Concurrency Patterns" {
		t.Errorf("first row = %q, want the best-ranked title", found[0].Text)
	}
	if found[maxTitles].Kind != KindQuery {
		t.Errorf("row %d is %q, want a completion once titles are capped", maxTitles, found[maxTitles].Kind)
	}
}

// The two sources reach the same phrase by different routes, and the ranked one wins.
func TestSuggestDropsACompletionThatRepeatsATitle(t *testing.T) {
	idx := suggestingBoth(t,
		[]string{"Docker Compose"},
		[]string{"docker compose", "docker image"})

	found := suggested(t, idx, "docker")

	want := []Suggestion{
		{Text: "Docker Compose", Kind: KindTitle},
		{Text: "docker image", Kind: KindQuery},
	}
	if !slices.Equal(found, want) {
		t.Errorf("got %v, want %v", found, want)
	}
}

// Titles are the source that answers nothing when the phrase is in no headline, which
// is the whole reason completions are still asked for.
func TestSuggestFallsBackToCompletionsWhenNoTitleMatches(t *testing.T) {
	idx := suggestingBoth(t, nil, []string{"why is my postgres slow", "why is my python broken"})

	found := suggested(t, idx, "why is my")

	if len(found) != 2 || found[0].Kind != KindQuery {
		t.Fatalf("got %v, want completions only", found)
	}
}

// One source being unreachable is a shorter list; only both failing is a failed request.
func TestSuggestSurvivesOneSourceFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/docs/suggest") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"value": [{"text": "x", "queryPlusText": "rust compiler"}]}`)
	}))
	defer srv.Close()

	found := suggested(t, New(srv.URL, "articles", "test-key", ""), "rust")

	if len(found) != 1 || found[0].Text != "rust compiler" {
		t.Errorf("got %v, want the source that answered", found)
	}
}

func TestSuggestFailsOnlyWhenBothSourcesDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "articles", "test-key", "").
		Suggest(context.Background(), "rust"); err == nil {
		t.Error("both sources failed and the request did not")
	}
}

// A title row is a query somebody might type. Three of them from one blog's series is
// one answer given three times, and a title long enough to be a paragraph is not a query
// at all.
func TestTitlesAreVariedAndShortEnoughToSearchFor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"value": [
			{"@search.text": "How to avoid A", "sourceId": "whisman"},
			{"@search.text": "How to avoid B", "sourceId": "whisman"},
			{"@search.text": "How to `+strings.Repeat("go on and ", 10)+`", "sourceId": "long"},
			{"@search.text": "How to debug", "sourceId": "other"}
		]}`)
	}))
	defer srv.Close()

	titles, err := New(srv.URL, "articles", "test-key", "").titles(context.Background(), "how to")
	if err != nil {
		t.Fatalf("titles: %v", err)
	}

	if want := []string{"How to avoid A", "How to debug"}; !slices.Equal(titles, want) {
		t.Errorf("got %q, want %q", titles, want)
	}
}

// The corpus is multilingual and search returns all of it. Three rows of a convenience
// are a different thing: a query in one writing system should not spend every one of
// them on titles the reader cannot read.
func TestTitlesMatchTheWritingSystemOfTheQuery(t *testing.T) {
	rows := `{"value": [
		{"@search.text": "Python 对象引用与复制 参考手册读书笔记", "sourceId": "a"},
		{"@search.text": "Python for Data Analysis", "sourceId": "b"},
		{"@search.text": "Python à la française", "sourceId": "c"}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, rows)
	}))
	defer srv.Close()
	idx := New(srv.URL, "articles", "test-key", "")

	latin, err := idx.titles(context.Background(), "python")
	if err != nil {
		t.Fatalf("titles: %v", err)
	}
	// Accented Latin is Latin: only the title that is mostly another script goes.
	want := []string{"Python for Data Analysis", "Python à la française"}
	if !slices.Equal(latin, want) {
		t.Errorf("latin query got %q, want %q", latin, want)
	}

	// A reader typing in another script has said what they read, so nothing is dropped.
	other, err := idx.titles(context.Background(), "对象")
	if err != nil {
		t.Fatalf("titles: %v", err)
	}
	if len(other) != 3 {
		t.Errorf("got %d titles for a non-Latin query, want all 3", len(other))
	}
}
