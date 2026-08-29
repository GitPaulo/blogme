package index

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The suggestion harness runs a fixed set of prefixes against a real index and prints
// the completions, so that a change to what gets offered can be compared against what
// it replaced rather than argued about. It is the ranking harness's counterpart, and
// runs the same way.
//
//	make suggest-harness
//	make suggest-harness PREFIXES="rust,kubernet"
//
// Alongside each list it reports what the service offered before the filter in
// worthOffering removed anything, which is the measure that matters: on the corpus as
// it stood, half of every eight rows a reader saw were "rust and", "kubernetes on",
// "python a" and "how to 2026".
//
// The prefixes are the shapes a search box actually receives: a whole word, a word
// part-way typed, a phrase, a question opener, and a person's name.
var suggestPrefixes = []string{
	"rust",
	"kubernet",
	"postgres",
	"python",
	"docker",
	"react",
	"go conc",
	"distributed sys",
	"machine learn",
	"web perf",
	"llm",
	"how to",
	"why is",
	"sean",
}

func TestSuggestionHarness(t *testing.T) {
	endpoint := os.Getenv("BLOGME_SEARCH_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BLOGME_SEARCH_ENDPOINT to run the suggestion harness")
	}

	// No semantic configuration: autocomplete never reranks, and naming one here would
	// only invite the thought that it might.
	idx := New(endpoint, envOr("BLOGME_SEARCH_INDEX", "articles"),
		os.Getenv("BLOGME_SEARCH_API_KEY"), "")

	prefixes := suggestPrefixes
	if custom := os.Getenv("BLOGME_HARNESS_PREFIXES"); custom != "" {
		prefixes = strings.Split(custom, ",")
	}

	// Long enough that the harness reports a slow service rather than failing on it.
	suggestTimeout = 30 * time.Second
	t.Cleanup(func() { suggestTimeout = 1500 * time.Millisecond })

	var offered, kept int
	var slowest time.Duration

	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}

		started := time.Now()
		rows, err := idx.Suggest(context.Background(), prefix)
		took := time.Since(started)
		if err != nil {
			t.Fatalf("suggest %q: %v", prefix, err)
		}

		merged := make([]string, 0, len(rows))
		for _, row := range rows {
			// Marked so the two sources can be told apart at a glance: a title is one
			// somebody wrote and the service ranked, a completion one the suggester
			// assembled out of a pair of terms.
			mark := " "
			if row.Kind == KindTitle {
				mark = ">"
			}
			merged = append(merged, mark+row.Text)
		}
		if took > slowest {
			slowest = took
		}

		// What the service would have returned unfiltered, for the same prefix and the
		// same number of rows. Read through the same client so the comparison is of the
		// filter and nothing else.
		raw, err := idx.rawCompletions(context.Background(), prefix, maxSuggestions)
		if err != nil {
			t.Fatalf("raw completions %q: %v", prefix, err)
		}
		for _, term := range raw {
			offered++
			if worthOffering(term, prefix) {
				kept++
			}
		}

		fmt.Printf("\nsuggest %-18q %4dms  %d/%d rows\n",
			prefix, took.Milliseconds(), len(rows), maxSuggestions)
		fmt.Printf("  before  %s\n", strings.Join(raw, " · "))
		fmt.Printf("  after   %s\n", strings.Join(merged, " · "))
	}

	if offered == 0 {
		t.Fatal("the index returned nothing for any prefix; is it backfilled?")
	}
	fmt.Printf("\n--- %d of %d rows the service offers survive the filter (%.0f%%), "+
		"slowest prefix %dms\n", kept, offered, 100*float64(kept)/float64(offered),
		slowest.Milliseconds())
}
