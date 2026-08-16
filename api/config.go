package main

import "os"

// config holds the app settings read from the environment. In Azure these come from
// Function App application settings; locally from local.settings.json.
type config struct {
	sourcesPath       string
	articlesContainer string
	searchEndpoint    string
	searchIndex       string
	searchAPIKey      string
	discoverySchedule string
}

func loadConfig() config {
	return config{
		sourcesPath:       env("BLOGME_SOURCES_PATH", "../sources/blogs.yml"),
		articlesContainer: env("BLOGME_ARTICLES_CONTAINER", "articles"),
		searchEndpoint:    os.Getenv("BLOGME_SEARCH_ENDPOINT"),
		searchIndex:       env("BLOGME_SEARCH_INDEX", "articles"),
		searchAPIKey:      os.Getenv("BLOGME_SEARCH_API_KEY"),
		discoverySchedule: env("BLOGME_DISCOVERY_SCHEDULE", "0 0 */6 * * *"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
