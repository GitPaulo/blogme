#!/usr/bin/env bash
# Repairs article authors copied from a source name that was wrong.
#
# The source name is the author fallback in crawl.go and sitemap.go, so a source named
# wrongly puts that name on every post it has. Fixing the extractor stops it recurring
# but does not reach documents already indexed: toArticle calls skipStored before it
# builds an article, so a re-crawl never rewrites their author.
#
#   infra/repair-authors.sh --mode interstitial     names read off a bot-check page
#   infra/repair-authors.sh --mode platform         *.github.io blogs named "Github"
#
# Dry run by default; add --apply to write. Safe to re-run: a document whose author is
# already correct no longer matches, so a second pass finds nothing.

set -euo pipefail

RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
SEARCH_SERVICE="${SEARCH_SERVICE:-srch-blogme-basic-b3d38b}"
SOURCES="${SOURCES:-sources/blogs.yml}"
ROLLBACK="${ROLLBACK:-author-repair-rollback.json}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }

[[ -f "$SOURCES" ]] || die "$SOURCES not found; run from the repository root"

# Git Bash on Windows ships "python" and no "python3", and the shim it does install
# under that name only offers to open the Microsoft Store.
PYTHON="${PYTHON:-}"
if [[ -z "$PYTHON" ]]; then
	for candidate in python3 python; do
		if "$candidate" -c 'import sys; sys.exit(0)' >/dev/null 2>&1; then
			PYTHON="$candidate"
			break
		fi
	done
fi
[[ -n "$PYTHON" ]] || die "no python interpreter found; set PYTHON=/path/to/python"

log "Service"
echo "  service: $SEARCH_SERVICE"
echo "  sources: $SOURCES"

BLOGME_SEARCH_API_KEY="$(az search admin-key show \
	--service-name "$SEARCH_SERVICE" --resource-group "$RESOURCE_GROUP" \
	--query primaryKey -o tsv)"
BLOGME_SEARCH_ENDPOINT="https://${SEARCH_SERVICE}.search.windows.net"
export BLOGME_SEARCH_API_KEY BLOGME_SEARCH_ENDPOINT

log "Repair"
exec "$PYTHON" "$(dirname "$0")/repair_authors.py" \
	--sources "$SOURCES" --rollback "$ROLLBACK" "$@"
