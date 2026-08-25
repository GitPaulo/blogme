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
		for i, r := range page.Results {
			// Undated is worth seeing at a glance: it is what nearly every landing
			// page in the corpus has in common.
			date := "          "
			if !r.PublishedAt.IsZero() {
				date = r.PublishedAt.Format("2006-01-02")
			}
			fmt.Printf("  %2d  %8.3f  %s  %-22s  %s\n",
				i+1, r.Score, date, truncate(r.Author, 22), truncate(r.Title, 64))
		}
	}
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
