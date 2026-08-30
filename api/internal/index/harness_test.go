package index

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The ranking harness runs a fixed set of queries against a real index and prints
// what comes back, so that a change to ranking can be compared against the ordering
// it replaced rather than argued about.
//
// It is a test only because that is the one way to run code against this package
// without adding a second main package to the module, which the Functions host
// builds. It never runs in CI: without an endpoint it skips, and CI has none.
//
//	make harness                                  # the index's own default profile
//	make harness PROFILE=relevance-quality        # with the quality boost
//	make harness PROFILE=relevance-authorlight MODE=semantic
//
// Compare two runs by their output. The queries below are the ones on record: the
// most searched, the ones used to tune ranking before, and the three that showed
// landing pages beating articles.
var harnessQueries = []string{
	"python",
	"security",
	"rust ownership",
	"github actions",
	"sean goedecke",
	"claude",
	"golang",
}

func TestRankingHarness(t *testing.T) {
	endpoint := os.Getenv("BLOGME_SEARCH_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BLOGME_SEARCH_ENDPOINT to run the ranking harness")
	}

	rank := RankKeyword
	if os.Getenv("BLOGME_HARNESS_MODE") == RankSemantic {
		rank = RankSemantic
	}

	// The semantic configuration is only named when semantic ranking was asked for,
	// so a keyword run cannot quietly spend the metered reranking budget.
	semantic := ""
	if rank == RankSemantic {
		semantic = envOr("BLOGME_SEARCH_SEMANTIC_CONFIG", "blogme-semantic")
	}

	idx := New(endpoint, envOr("BLOGME_SEARCH_INDEX", "articles"),
		os.Getenv("BLOGME_SEARCH_API_KEY"), semantic)

	profile := os.Getenv("BLOGME_HARNESS_PROFILE")
	queries := harnessQueries
	if custom := os.Getenv("BLOGME_HARNESS_QUERIES"); custom != "" {
		queries = strings.Split(custom, ",")
	}

	fmt.Printf("\nindex %s   profile %s   ranking %s\n",
		envOr("BLOGME_SEARCH_INDEX", "articles"), or(profile, "<index default>"), rank)

	for _, q := range queries {
		q = strings.TrimSpace(q)
		page, err := idx.Query(context.Background(), q, QueryOptions{
			Limit:   10,
			Fetch:   30,
			Rank:    rank,
			Profile: profile,
		})
		if err != nil {
			t.Errorf("query %q: %v", q, err)
			continue
		}

		fmt.Printf("\n%s  (%d matches, %d documents read)\n", strings.ToUpper(q), page.Total, page.Read)
		stubs, named := 0, 0
		for i, r := range page.Results {
			// Undated is worth seeing at a glance: it is what nearly every landing
			// page in the corpus has in common.
			date := "          "
			if !r.PublishedAt.IsZero() {
				date = r.PublishedAt.Format("2006-01-02")
			}
			if len(strings.Fields(r.Title)) <= 2 {
				stubs++
			}
			if sharesAWord(r.Author, q) {
				named++
			}
			fmt.Printf("  %2d  %8.3f  %s  %-22s  %s\n",
				i+1, r.Score, date, truncate(r.Author, 22), truncate(r.Title, 64))
		}
		// Two counts rather than one, because reading either alone points the wrong way.
		//
		// A page of blogs named after the query looks like the author weight running
		// away with the ranking, and lowering that weight does cut the number right
		// down. It also fills the page with posts whose whole title is the query word —
		// "Python", "Linux", "Testing" — because the author field is what was holding
		// those back. The two counts move in opposite directions, and a change that
		// improves one at the other's expense has usually made search worse.
		//
		// Printed on every query so nobody has to know that in advance. See
		// docs/quality-scoring.md for the grid this came from.
		if n := len(page.Results); n > 0 {
			fmt.Printf("      %d/%d titles of two words or fewer, %d/%d bylines sharing a word with the query\n",
				stubs, n, named, n)
		}
	}
}

// sharesAWord reports whether a byline contains one of the words searched for, which is
// how a blog named after the query is spotted. Short words are ignored: "a" and "of"
// appear in bylines everywhere and mean nothing there.
func sharesAWord(author, query string) bool {
	words := make(map[string]struct{})
	for _, w := range strings.Fields(strings.ToLower(author)) {
		words[strings.Trim(w, `.,:;!?()[]{}"'`)] = struct{}{}
	}
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if len(w) <= 2 {
			continue
		}
		if _, ok := words[w]; ok {
			return true
		}
	}
	return false
}

func envOr(key, fallback string) string {
	return or(os.Getenv(key), fallback)
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
