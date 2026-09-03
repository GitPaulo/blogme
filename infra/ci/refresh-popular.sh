#!/usr/bin/env bash
# Rebuilds the landing page's list of widely shared blogs.
#
# Downloads the site standing map the scoring timer writes, resolves a search query key,
# and runs the generator against them and the committed source list. The generator
# writes nothing unless it can vouch for a full list, so a failure here leaves
# web/src/lib/data/popular.json exactly as it was.
#
# Run by .github/workflows/refresh-popular.yml and by `make popular`. The two differ
# only in which Python they hand it: CI installs the requirements globally, the Makefile
# points PYTHON at the extractor's venv.

set -euo pipefail

# Match the defaults in infra/provision.sh.
RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
FUNCTION_APP="${FUNCTION_APP:-func-blogme-b3d38b}"
SOURCES_CONTAINER="${SOURCES_CONTAINER:-sources}"
POPULARITY_BLOB="${POPULARITY_BLOB:-popularity.json}"
PYTHON="${PYTHON:-python3}"
# Where to leave the Markdown report for a pull request body. Unset means don't write one.
REPORT="${REPORT:-}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }

# Paths below are repo-relative, so the caller's working directory cannot change what
# this reads or writes.
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

log "Storage account"
STORAGE_ACCOUNT="${STORAGE_ACCOUNT:-$(az functionapp config appsettings list \
	--name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" \
	--query "[?name=='BLOGME_STORAGE_ACCOUNT'].value | [0]" -o tsv)}"
[[ -n "$STORAGE_ACCOUNT" ]] \
	|| die "could not resolve BLOGME_STORAGE_ACCOUNT from $FUNCTION_APP; run 'az login'"
echo "  $STORAGE_ACCOUNT"

log "Downloading $POPULARITY_BLOB"
# --auth-mode key, not login: infra/github-oidc.sh grants CI Contributor, which is the
# management plane. Reading a blob as `login` is the data plane and would need a
# Storage Blob Data Reader assignment on top; listing the account key does not.
az storage blob download --account-name "$STORAGE_ACCOUNT" \
	--container-name "$SOURCES_CONTAINER" --name "$POPULARITY_BLOB" \
	--file "$workdir/popularity.json" \
	--auth-mode key --no-progress --output none
printf '  %s bytes\n' "$(wc -c <"$workdir/popularity.json")"

log "Search service"
SEARCH_SERVICE="${SEARCH_SERVICE:-$(az search service list \
	--resource-group "$RESOURCE_GROUP" --query '[0].name' -o tsv)}"
[[ -n "$SEARCH_SERVICE" ]] || die "no search service in $RESOURCE_GROUP"
BLOGME_SEARCH_ENDPOINT="${BLOGME_SEARCH_ENDPOINT:-https://${SEARCH_SERVICE}.search.windows.net}"
# A query key, not the admin key: this only counts documents.
BLOGME_SEARCH_API_KEY="${BLOGME_SEARCH_API_KEY:-$(az search query-key list \
	--service-name "$SEARCH_SERVICE" --resource-group "$RESOURCE_GROUP" \
	--query '[0].key' -o tsv)}"
[[ -n "$BLOGME_SEARCH_API_KEY" ]] || die "could not read a query key for $SEARCH_SERVICE"
export BLOGME_SEARCH_ENDPOINT BLOGME_SEARCH_API_KEY
echo "  $BLOGME_SEARCH_ENDPOINT"

log "Building the list"
# No --allow-unchecked. Unattended, a list that cannot be vouched for has to stop the
# run rather than reach the front page; see the refusals in build_popular.py.
args=(--popularity "$workdir/popularity.json")
if [[ -n "$REPORT" ]]; then
	args+=(--summary "$REPORT")
fi
"$PYTHON" sources/tools/build_popular.py "${args[@]}"
