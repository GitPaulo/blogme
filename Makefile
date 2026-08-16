# blogme — one entrypoint for both applications.
# Run `make` or `make help` to list targets.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Raises the GitHub API limits the source extractor runs into.
GITHUB_TOKEN ?= $(shell gh auth token 2>/dev/null)
export GITHUB_TOKEN

SOURCES_LOG := sources/tools/build.log

.PHONY: help setup dev check build clean \
        check-api check-web build-api build-web fmt sources sources-status

help: ## List available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

setup: ## Install dependencies and create local settings
	cd api && go mod download
	cd web && pnpm install
	@[[ -f api/local.settings.json ]] \
		|| cp api/local.settings.sample.json api/local.settings.json
	@[[ -f web/.env ]] || cp web/.env.example web/.env

dev: ## Run Azurite, the Functions host and the Vite dev server together
	@trap 'kill 0' EXIT INT TERM; \
	azurite --silent --location .azurite & \
	(cd api && func start) & \
	(cd web && pnpm dev) & \
	wait

check: check-api check-web ## Lint, type-check and test everything

check-api: ## Vet, lint and test the Go API
	cd api && gofmt -l . | (! grep .)
	cd api && go vet ./...
	cd api && golangci-lint run ./...
	cd api && go test ./...

check-web: ## Type-check and lint the web app
	cd web && pnpm run check
	cd web && pnpm run lint

build: build-api build-web ## Build deployable artefacts for both apps

build-api: ## Package the Functions app for Linux x64
	cd api && func pack

build-web: ## Build the static site
	cd web && pnpm run build

fmt: ## Format all source
	cd api && gofmt -w .
	cd web && pnpm run format

sources: ## Rebuild sources/blogs.yml in the background (takes hours)
	cd sources/tools && { [[ -d .venv ]] || python3 -m venv .venv; } \
		&& .venv/bin/pip install -q -r requirements.txt
	cd sources/tools && nohup .venv/bin/python build_sources.py >/dev/null 2>build.log &
	@sleep 1 && echo "Started. Check on it with: make sources-status"

sources-status: ## Show progress of the background source rebuild
	@pgrep -f build_sources.py >/dev/null \
		&& echo "status: running" \
		|| echo "status: not running"
	@tail -n 3 $(SOURCES_LOG) 2>/dev/null || echo "no log at $(SOURCES_LOG) yet"

clean: ## Remove build output and local emulator state
	rm -rf api/bin api/*.zip web/build web/.svelte-kit .azurite
