package main

import (
	"context"
	"log/slog"

	"github.com/azure/azure-functions-golang-worker/sdk"
	"github.com/azure/azure-functions-golang-worker/sdk/bindings"
	"github.com/azure/azure-functions-golang-worker/worker"

	"github.com/GitPaulo/blogme/api/internal/discovery"
	"github.com/GitPaulo/blogme/api/internal/httpapi"
	"github.com/GitPaulo/blogme/api/internal/index"
	"github.com/GitPaulo/blogme/api/internal/store"
)

// Kept deliberately thin: configuration wiring and trigger registration only, so the
// domain code under internal/ is not coupled to the Azure Functions worker.
func main() {
	cfg := loadConfig()

	idx := index.New(cfg.searchEndpoint, cfg.searchIndex, cfg.searchAPIKey)
	st := store.New(cfg.articlesContainer)
	handlers := httpapi.New(idx)
	discoverer := discovery.New(cfg.sourcesPath, st, idx)

	app := sdk.FunctionApp()

	app.HTTP("search", handlers.Search,
		sdk.WithMethods("GET"),
		sdk.WithAuth("anonymous"),
	)

	app.HTTP("health", handlers.Health,
		sdk.WithMethods("GET"),
		sdk.WithAuth("anonymous"),
	)

	app.Timer("discover", func(ctx context.Context, timer bindings.TimerInfo) error {
		if timer.IsPastDue {
			slog.WarnContext(ctx, "discovery run is past due")
		}
		return discoverer.Run(ctx)
	}, sdk.WithSchedule(cfg.discoverySchedule))

	worker.Start(app)
}
