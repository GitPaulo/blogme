package main

import (
	"context"
	"log"
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

	idx := index.New(cfg.searchEndpoint, cfg.searchIndex, cfg.searchAPIKey)
	handlers := httpapi.New(idx)

	app := sdk.FunctionApp()

	app.HTTP("search", handlers.Search,
		sdk.WithMethods("GET"),
		sdk.WithAuth("anonymous"),
	)

	app.HTTP("health", handlers.Health,
		sdk.WithMethods("GET"),
		sdk.WithAuth("anonymous"),
	)

	discoverer, err := newDiscoverer(cfg, idx)
	if err != nil {
		log.Fatalf("configure discovery: %v", err)
	}

	app.Timer("discover", func(ctx context.Context, timer bindings.TimerInfo) error {
		if timer.IsPastDue {
			slog.WarnContext(ctx, "discovery run is past due")
		}
		return discoverer.Run(ctx)
	}, sdk.WithSchedule(cfg.discoverySchedule))

	worker.Start(app)
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
		cfg.discoveryBatch,
	), nil
}
