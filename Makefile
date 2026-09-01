# blogme — one entrypoint for both applications.
# Run `make` or `make help` to list targets.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Raises the GitHub API limits the source extractor runs into.
GITHUB_TOKEN ?= $(shell gh auth token 2>/dev/null)
export GITHUB_TOKEN

SOURCES_LOG := sources/tools/build.log

# A Windows venv puts its executables in Scripts/ rather than bin/, and ships no python3
# alias — the name exists only as a Microsoft Store stub that cannot create a venv.
VENV_BIN := $(if $(filter Windows_NT,$(OS)),Scripts,bin)
PYTHON ?= $(if $(filter Windows_NT,$(OS)),python,python3)

# Match the defaults in infra/provision.sh.
RESOURCE_GROUP ?= rg-blogme
FUNCTION_APP ?= func-blogme-b3d38b
SEARCH_SERVICE ?= srch-blogme-basic-b3d38b

.PHONY: help setup dev check build clean kill revive harness suggest-harness \
        check-api check-web build-api build-web fmt sources sources-status sources-upload \
        popular

help: ## List available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

setup: ## Install dependencies and create local settings
	cd api && go mod download
	cd web && pnpm install
	@[[ -f api/local.settings.json ]] \
		|| cp api/local.settings.sample.json api/local.settings.json
	@[[ -f web/.env ]] || cp web/.env.example web/.env

# There is no emulator for Azure AI Search, so a dev host reads the live index. The key
# is a query key rather than the admin key the harnesses use: search and suggest only
# read, and a read-only key is the one worth leaving in a shell. Both are taken from the
# environment first, so pointing a session at another service needs no edit here.
#
# Without them the app falls back to managed identity, which locally means shelling out
# to `az` for a token — slower than the suggest timeout allows, so completions fail on a
# deadline rather than on the missing setting.
dev: ## Run Azurite, the Functions host and the Vite dev server together
	@export BLOGME_SEARCH_ENDPOINT="$${BLOGME_SEARCH_ENDPOINT:-https://$(SEARCH_SERVICE).search.windows.net}"; \
	export BLOGME_SEARCH_API_KEY="$${BLOGME_SEARCH_API_KEY:-$$(az search query-key list \
		--service-name $(SEARCH_SERVICE) --resource-group $(RESOURCE_GROUP) \
		--query '[0].key' -o tsv 2>/dev/null)}"; \
	[[ -n "$$BLOGME_SEARCH_API_KEY" ]] \
		|| echo "warning: no search key — run 'az login'; search and suggest will fail"; \
	trap 'kill 0' EXIT INT TERM; \
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

check-web: ## Type-check, lint and test the web app
	cd web && pnpm run check
	cd web && pnpm run lint
	cd web && pnpm run test

build: build-api build-web ## Build deployable artefacts for both apps

build-api: ## Package the Functions app for Linux x64
	cd api && func pack

build-web: ## Build the static site
	cd web && pnpm run build

fmt: ## Format all source
	cd api && gofmt -w .
	cd web && pnpm run format

sources: ## Rebuild sources/blogs.yml in the background (long-running, uses several cores)
	cd sources/tools && { [[ -d .venv ]] || $(PYTHON) -m venv .venv; } \
		&& .venv/$(VENV_BIN)/pip install -q -r requirements.txt
	cd sources/tools && nohup .venv/$(VENV_BIN)/python build_sources.py >/dev/null 2>build.log &
	@sleep 1 && echo "Started. Check on it with: make sources-status"

sources-status: ## Show progress of the background source rebuild
	@if ! command -v pgrep >/dev/null 2>&1; then \
		echo "status: unknown (no pgrep here), read it from the log below"; \
	elif pgrep -f build_sources.py >/dev/null; then \
		echo "status: running"; \
	else \
		echo "status: not running"; \
	fi
	@tail -n 3 $(SOURCES_LOG) 2>/dev/null || echo "no log at $(SOURCES_LOG) yet"

sources-upload: ## Publish sources/blogs.yml to blob storage (no redeploy needed)
	@RESOURCE_GROUP=$(RESOURCE_GROUP) FUNCTION_APP=$(FUNCTION_APP) ./infra/upload-sources.sh

popular: ## Rebuild the landing page's list of widely shared blogs
	@STORAGE_ACCOUNT="$${STORAGE_ACCOUNT:-$$(az functionapp config appsettings list \
		--name $(FUNCTION_APP) --resource-group $(RESOURCE_GROUP) \
		--query "[?name=='BLOGME_STORAGE_ACCOUNT'].value | [0]" -o tsv 2>/dev/null)}"; \
	[[ -n "$$STORAGE_ACCOUNT" ]] \
		|| { echo "error: could not resolve the storage account, run 'az login'"; exit 1; }; \
	tmp="$$(mktemp)"; trap 'rm -f "$$tmp"' EXIT; \
	az storage blob download --account-name "$$STORAGE_ACCOUNT" \
		--container-name sources --name popularity.json --file "$$tmp" \
		--auth-mode login --no-progress --output none; \
	export BLOGME_SEARCH_ENDPOINT="$${BLOGME_SEARCH_ENDPOINT:-https://$(SEARCH_SERVICE).search.windows.net}"; \
	export BLOGME_SEARCH_API_KEY="$${BLOGME_SEARCH_API_KEY:-$$(az search query-key list \
		--service-name $(SEARCH_SERVICE) --resource-group $(RESOURCE_GROUP) \
		--query '[0].key' -o tsv 2>/dev/null)}"; \
	[[ -n "$$BLOGME_SEARCH_API_KEY" ]] \
		|| echo "warning: no search key, blogs will not be checked for articles"; \
	cd sources/tools && { [[ -d .venv ]] || $(PYTHON) -m venv .venv; } \
		&& .venv/$(VENV_BIN)/pip install -q -r requirements.txt \
		&& .venv/$(VENV_BIN)/python build_popular.py --popularity "$$tmp"

suggest-harness: ## Print what a fixed set of prefixes completes to (PREFIXES=a,b to override)
	@cd api && BLOGME_SEARCH_ENDPOINT="https://$(SEARCH_SERVICE).search.windows.net" \
		BLOGME_SEARCH_API_KEY="$$(az search admin-key show --service-name $(SEARCH_SERVICE) \
			--resource-group $(RESOURCE_GROUP) --query primaryKey -o tsv)" \
		BLOGME_HARNESS_PREFIXES="$(PREFIXES)" \
		go test ./internal/index -run TestSuggestionHarness -v -count=1 2>&1 \
		| sed -n '/^suggest /,/^--- /p'

harness: ## Print how a fixed query set ranks (PROFILE=... MODE=semantic to compare)
	@cd api && BLOGME_SEARCH_ENDPOINT="https://$(SEARCH_SERVICE).search.windows.net" \
		BLOGME_SEARCH_API_KEY="$$(az search admin-key show --service-name $(SEARCH_SERVICE) \
			--resource-group $(RESOURCE_GROUP) --query primaryKey -o tsv)" \
		BLOGME_HARNESS_PROFILE="$(PROFILE)" BLOGME_HARNESS_MODE="$(MODE)" \
		go test ./internal/index -run TestRankingHarness -v -count=1 2>&1 \
		| sed -n '/^index /,/^--- /p'

kill: ## Stop the app: search, health and discovery all refuse
	@RESOURCE_GROUP=$(RESOURCE_GROUP) FUNCTION_APP=$(FUNCTION_APP) ./infra/kill-switch.sh stop

revive: ## Start the app again after a kill
	@RESOURCE_GROUP=$(RESOURCE_GROUP) FUNCTION_APP=$(FUNCTION_APP) ./infra/kill-switch.sh start

clean: ## Remove build output and local emulator state
	rm -rf api/bin api/*.zip web/build web/.svelte-kit .azurite
