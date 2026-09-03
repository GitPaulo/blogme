#!/usr/bin/env bash
# Rebuilds the four posts at the top of the landing page.
#
# Asks Hacker News what is being read this week and keeps the ones already in the index.
# Cheaper than its companion in every way: no blob to download, one request to a free
# public API, and a handful of index lookups. Only the search key needs Azure at all.
#
# Run by .github/workflows/refresh-trending.yml and by `make trending`. The two differ
# only in which Python they hand it: CI installs the requirements globally, the Makefile
# points PYTHON at the extractor's venv.

set -euo pipefail

# Match the defaults in infra/provision.sh.
RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
PYTHON="${PYTHON:-python3}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }

# Paths below are repo-relative, so the caller's working directory cannot change what
# this reads or writes.
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

log "Search service"
SEARCH_SERVICE="${SEARCH_SERVICE:-$(az search service list \
	--resource-group "$RESOURCE_GROUP" --query '[0].name' -o tsv)}"
[[ -n "$SEARCH_SERVICE" ]] || die "no search service in $RESOURCE_GROUP; run 'az login'"
BLOGME_SEARCH_ENDPOINT="${BLOGME_SEARCH_ENDPOINT:-https://${SEARCH_SERVICE}.search.windows.net}"
# A query key, not the admin key: this only looks documents up by id.
BLOGME_SEARCH_API_KEY="${BLOGME_SEARCH_API_KEY:-$(az search query-key list \
	--service-name "$SEARCH_SERVICE" --resource-group "$RESOURCE_GROUP" \
	--query '[0].key' -o tsv)}"
[[ -n "$BLOGME_SEARCH_API_KEY" ]] || die "could not read a query key for $SEARCH_SERVICE"
export BLOGME_SEARCH_ENDPOINT BLOGME_SEARCH_API_KEY
echo "  $BLOGME_SEARCH_ENDPOINT"

log "Building the section"
# The generator writes nothing unless it can vouch for every row, so a failure here
# leaves the committed section exactly as it was.
"$PYTHON" sources/tools/build_trending.py
