package main

import (
	"context"
	"log/slog"

	"github.com/azure/azure-functions-golang-worker/sdk"
	"github.com/azure/azure-functions-golang-worker/sdk/bindings"
	"github.com/azure/azure-functions-golang-worker/worker"

	"github.com/GitPaulo/blogme/api/internal/blob"
	"github.com/GitPaulo/blogme/api/internal/discovery"
	"github.com/GitPaulo/blogme/api/internal/httpapi"
	"github.com/GitPaulo/blogme/api/internal/index"
	"github.com/GitPaulo/blogme/api/internal/sources"
	"github.com/GitPaulo/blogme/api/internal/store"
)

// Kept deliberately thin: configuration wiring and trigger registration only, so the
// domain code under internal/ is not coupled to the Azure Functions worker.
func main() {
	cfg := loadConfig()

	idx := index.New(cfg.searchEndpoint, cfg.searchIndex, cfg.searchAPIKey, cfg.searchSemantic)
	handlers := httpapi.New(idx, cfg.searchLimits)

	app := sdk.FunctionApp()

	app.HTTP("search", handlers.Search,
		sdk.WithMethods("GET"),
		sdk.WithAuth("anonymous"),
	)

	app.HTTP("health", handlers.Health,
		sdk.WithMethods("GET"),
		sdk.WithAuth("anonymous"),
	)

	// The write path must not be able to take the read path down with it: a discovery
	// that cannot be configured leaves search serving whatever is already indexed, and
	// simply registers no timer.
	discoveryState := "enabled"
	if discoverer, err := newDiscoverer(cfg, idx); err != nil {
		slog.Error("discovery disabled", "error", err)
		discoveryState = "disabled"
	} else {
		app.Timer("discover", func(ctx context.Context, timer bindings.TimerInfo) error {
			if timer.IsPastDue {
				slog.WarnContext(ctx, "discovery run is past due")
			}
			return discoverer.Run(ctx)
		}, sdk.WithSchedule(cfg.discoverySchedule))
	}

	// One line at startup, so an instance can say what it actually is: which index it
	// talks to, whether reranking and discovery are on, and how hard it will crawl.
	// Half of a confusing incident is finding out the deployed settings were not the
	// ones you thought. No credential appears here, only whether one is in use.
	//
	// Not a *Context call: this runs before any invocation exists, so there is no
	// invocation to correlate it with.
	slog.Info("blogme starting",
		"search_index", cfg.searchIndex,
		"search_auth", searchAuth(cfg),
		"semantic", cfg.searchSemantic != "",
		"sources", sourcesOrigin(cfg),
		"discovery", discoveryState,
		"schedule", cfg.discoverySchedule,
		"batch", cfg.discoveryBatch,
		"crawl_concurrency", cfg.crawlConcurrency)

	worker.Start(app)
}

// searchAuth names how the index is authenticated without naming the secret.
func searchAuth(cfg config) string {
	if cfg.searchAPIKey != "" {
		return "api-key"
	}
	return "managed-identity"
}

// sourcesOrigin reports where the approved source list is read from.
func sourcesOrigin(cfg config) string {
	if cfg.sourcesPath != "" {
		return "file"
	}
	return "blob"
}

func newDiscoverer(cfg config, idx *index.Index) (*discovery.Discoverer, error) {
	client, err := blob.New(cfg.storageAccount)
	if err != nil {
		return nil, err
	}

	// A local file keeps the dev loop free of any storage dependency.
	var provider sources.Provider = sources.NewBlobProvider(client, cfg.sourcesContainer, cfg.sourcesBlob)
	if cfg.sourcesPath != "" {
		provider = &sources.FileProvider{Path: cfg.sourcesPath}
	}

	return discovery.New(
		provider,
		store.New(client, cfg.articlesContainer),
		idx,
		discovery.NewCursor(client, cfg.sourcesContainer, cfg.cursorBlob),
		discovery.Options{
			BatchSize:    cfg.discoveryBatch,
			MaxPosts:     cfg.maxPostsPerSource,
			ContentWords: cfg.contentWords,
			Concurrency:  cfg.crawlConcurrency,
		},
	), nil
}
