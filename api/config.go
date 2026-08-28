package main

import (
	"os"
	"strconv"

	"github.com/GitPaulo/blogme/api/internal/httpapi"
)

// config holds the app settings read from the environment. In Azure these come from
// Function App application settings; locally from local.settings.json.
type config struct {
	storageAccount    string
	sourcesContainer  string
	sourcesBlob       string
	cursorBlob        string
	articlesContainer string
	// When set, the source list is read from this local file instead of blob storage.
	sourcesPath    string
	searchEndpoint string
	searchIndex    string
	searchAPIKey   string
	// Name of the index's semantic configuration. Empty turns reranking off, which is
	// the escape hatch if the metered quota becomes a problem.
	searchSemantic    string
	discoverySchedule string
	discoveryBatch    int
	maxPostsPerSource int
	contentWords      int
	crawlConcurrency  int
	// Quality scoring. It reads and writes the index and nothing else, so the only
	// settings it needs are when to run and how much to do in one pass.
	qualitySchedule   string
	qualityScoreBatch int
	qualitySweepBatch int
	popularityBlob    string
	// Search throttling. The endpoint is anonymous, so these bound what one caller
	// can spend; see api/internal/httpapi/ratelimit.go.
	searchLimits httpapi.Limits
}

func loadConfig() config {
	return config{
		storageAccount:    os.Getenv("BLOGME_STORAGE_ACCOUNT"),
		sourcesContainer:  env("BLOGME_SOURCES_CONTAINER", "sources"),
		sourcesBlob:       env("BLOGME_SOURCES_BLOB", "blogs.yml"),
		cursorBlob:        env("BLOGME_CURSOR_BLOB", "discovery-cursor"),
		articlesContainer: env("BLOGME_ARTICLES_CONTAINER", "articles"),
		sourcesPath:       os.Getenv("BLOGME_SOURCES_PATH"),
		searchEndpoint:    os.Getenv("BLOGME_SEARCH_ENDPOINT"),
		searchIndex:       env("BLOGME_SEARCH_INDEX", "articles"),
		searchAPIKey:      os.Getenv("BLOGME_SEARCH_API_KEY"),
		searchSemantic:    env("BLOGME_SEARCH_SEMANTIC_CONFIG", "blogme-semantic"),
		discoverySchedule: env("BLOGME_DISCOVERY_SCHEDULE", "0 0 */6 * * *"),
		discoveryBatch:    envInt("BLOGME_DISCOVERY_BATCH", 200),
		maxPostsPerSource: envInt("BLOGME_MAX_POSTS_PER_SOURCE", 15),
		contentWords:      envInt("BLOGME_CONTENT_WORDS", 1000),
		crawlConcurrency:  envInt("BLOGME_CRAWL_CONCURRENCY", 16),
		// Half past the hour, so a scoring pass and a discovery pass are not reading
		// and writing the same index at the same moment.
		qualitySchedule:   env("BLOGME_QUALITY_SCHEDULE", "0 30 * * * *"),
		qualityScoreBatch: envInt("BLOGME_QUALITY_SCORE_BATCH", 5000),
		qualitySweepBatch: envCount("BLOGME_QUALITY_SWEEP_BATCH", 2000),
		popularityBlob:    env("BLOGME_POPULARITY_BLOB", "popularity.json"),
		searchLimits:      searchLimits(),
	}
}

// searchLimits reads the throttling settings, falling back per field so that
// raising one limit does not mean restating the rest.
func searchLimits() httpapi.Limits {
	d := httpapi.DefaultLimits()
	return httpapi.Limits{
		PerMinute:         envInt("BLOGME_SEARCH_RATE_PER_MINUTE", d.PerMinute),
		Burst:             envInt("BLOGME_SEARCH_RATE_BURST", d.Burst),
		AllPerMinute:      envInt("BLOGME_SEARCH_RATE_ALL_PER_MINUTE", d.AllPerMinute),
		AllBurst:          envInt("BLOGME_SEARCH_RATE_ALL_BURST", d.AllBurst),
		SemanticPerMinute: envInt("BLOGME_SEMANTIC_RATE_PER_MINUTE", d.SemanticPerMinute),
		SemanticBurst:     envInt("BLOGME_SEMANTIC_RATE_BURST", d.SemanticBurst),
		SemanticPerHour:   envInt("BLOGME_SEMANTIC_RATE_PER_HOUR", d.SemanticPerHour),
		SemanticHourBurst: envInt("BLOGME_SEMANTIC_RATE_HOUR_BURST", d.SemanticHourBurst),

		SuggestPerMinute:    envInt("BLOGME_SUGGEST_RATE_PER_MINUTE", d.SuggestPerMinute),
		SuggestBurst:        envInt("BLOGME_SUGGEST_RATE_BURST", d.SuggestBurst),
		SuggestAllPerMinute: envInt("BLOGME_SUGGEST_RATE_ALL_PER_MINUTE", d.SuggestAllPerMinute),
		SuggestAllBurst:     envInt("BLOGME_SUGGEST_RATE_ALL_BURST", d.SuggestAllBurst),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

// envCount reads a setting whose zero is an answer rather than an omission. A batch
// of none means "do not do this at all", which envInt would read as unset and
// replace with the default.
func envCount(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v >= 0 {
		return v
	}
	return fallback
}
