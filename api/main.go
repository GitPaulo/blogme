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
	"github.com/GitPaulo/blogme/api/internal/quality"
	"github.com/GitPaulo/blogme/api/internal/sources"
	"github.com/GitPaulo/blogme/api/internal/store"
)

// main wires configuration to triggers and nothing else, so the domain code under
// internal/ stays free of the Azure Functions worker.
func main() {
	cfg := loadConfig()

	idx := index.New(cfg.searchEndpoint, cfg.searchIndex, cfg.searchAPIKey, cfg.searchSemantic)
	handlers := httpapi.New(idx, cfg.searchLimits)

	app := sdk.FunctionApp()

	// Compressed, because a page of results is the only answer here large enough for
	// it to matter; the health check's whole body is twenty bytes.
	app.HTTP("search", httpapi.Compress(handlers.Search),
		sdk.WithMethods("GET"),
		sdk.WithAuth("anonymous"),
	)

	app.HTTP("health", handlers.Health,
		sdk.WithMethods("GET"),
		sdk.WithAuth("anonymous"),
	)

	// Timers that cannot be configured simply do not register, leaving search to
	// serve whatever is already indexed. Schedules are NCRONTAB expressions:
	// https://learn.microsoft.com/azure/azure-functions/functions-bindings-timer
	timerState := "enabled"
	if jobs, err := newJobs(cfg, idx); err != nil {
		slog.Error("timers disabled", "error", err)
		timerState = "disabled"
	} else {
		app.Timer("discover", func(ctx context.Context, timer bindings.TimerInfo) error {
			if timer.IsPastDue {
				slog.WarnContext(ctx, "discovery run is past due")
			}
			return jobs.discoverer.Run(ctx)
		}, sdk.WithSchedule(cfg.discoverySchedule))

		// Registered separately from discovery so each can be turned off on its own,
		// and so a scoring failure cannot cost a crawl.
		app.Timer("score", func(ctx context.Context, timer bindings.TimerInfo) error {
			if timer.IsPastDue {
				slog.WarnContext(ctx, "quality run is past due")
			}
			return jobs.scorer.Run(ctx)
		}, sdk.WithSchedule(cfg.qualitySchedule))
	}

	// What this instance actually is: which index it talks to, what is switched on,
	// and how hard it will crawl. No credential appears here, only whether one is in
	// use. Not a *Context call, as no invocation exists yet to correlate it with.
	slog.Info("blogme starting",
		"search_index", cfg.searchIndex,
		"search_auth", searchAuth(cfg),
		"semantic", cfg.searchSemantic != "",
		"sources", sourcesOrigin(cfg),
		"timers", timerState,
		"schedule", cfg.discoverySchedule,
		"batch", cfg.discoveryBatch,
		"crawl_concurrency", cfg.crawlConcurrency,
		"quality_schedule", cfg.qualitySchedule,
		"quality_batch", cfg.qualityScoreBatch,
		"quality_version", quality.Version)

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

// jobs are the timer-driven halves of the service: the crawler that fills the corpus
// and the scorer that judges what is in it. Built together because they share the
// storage account and the source list, so one misconfiguration is reported once.
type jobs struct {
	discoverer *discovery.Discoverer
	scorer     *quality.Scorer
}

func newJobs(cfg config, idx *index.Index) (jobs, error) {
	client, err := blob.New(cfg.storageAccount)
	if err != nil {
		return jobs{}, err
	}

	// A local file keeps the dev loop free of any storage dependency.
	var provider sources.Provider = sources.NewBlobProvider(client, cfg.sourcesContainer, cfg.sourcesBlob)
	if cfg.sourcesPath != "" {
		provider = &sources.FileProvider{Path: cfg.sourcesPath}
	}

	discoverer := discovery.New(
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
	)

	// Popularity lives beside the source list rather than beside the articles: it is
	// keyed by site, changes on its own schedule, and is rebuildable from a public API.
	popularity := quality.NewStore(client, cfg.sourcesContainer, cfg.popularityBlob)

	scorer := quality.New(idx, provider, popularity, quality.Options{
		ScoreBatch: cfg.qualityScoreBatch,
		SweepBatch: cfg.qualitySweepBatch,
	})

	return jobs{discoverer: discoverer, scorer: scorer}, nil
}
