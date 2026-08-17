package main

import (
	"os"
	"strconv"
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
