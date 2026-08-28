#!/usr/bin/env bash
# Fills titleSuggest on articles indexed before the field existed, so typeahead has
# something to complete from. See backfill_suggest.py for why the field is a copy of
# title rather than title itself.
#
#   infra/backfill-suggest.sh                        # report what would be written
#   infra/backfill-suggest.sh --apply                # write it
#   infra/backfill-suggest.sh --apply --since 2023-01-01
#
# Dry run by default. Safe to re-run and safe to interrupt: a document leaves the set
# this walks by being written, so a second run picks up whatever the first did not
# reach.
#
# Apply the index schema first — create-search-index.sh, which the API deploy runs on
# its own — or every write fails on a field the index does not have.

set -euo pipefail

RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
SEARCH_SERVICE="${SEARCH_SERVICE:-srch-blogme-basic-b3d38b}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }

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

BLOGME_SEARCH_API_KEY="$(az search admin-key show \
	--service-name "$SEARCH_SERVICE" --resource-group "$RESOURCE_GROUP" \
	--query primaryKey -o tsv)"
BLOGME_SEARCH_ENDPOINT="https://${SEARCH_SERVICE}.search.windows.net"
export BLOGME_SEARCH_API_KEY BLOGME_SEARCH_ENDPOINT

log "Backfill"
exec "$PYTHON" "$(dirname "$0")/backfill_suggest.py" "$@"
