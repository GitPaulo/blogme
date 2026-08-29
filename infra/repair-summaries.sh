#!/usr/bin/env bash
# Repairs article summaries mangled by two extractor bugs.
#
# collectText put a space after every text node, so inline markup left one in front of
# the punctuation that followed it ("it's big ."), and it handed MDX comments through as
# prose, so cards led with "{/* eslint-disable react/jsx-no-undef */}". Fixing the
# extractor stops both recurring but does not reach documents already indexed: toArticle
# calls skipStored before it builds an article, so a re-crawl never rewrites a summary.
#
#   infra/repair-summaries.sh --mode mdx        the leaked MDX comments, exact
#   infra/repair-summaries.sh --mode spacing    the injected spaces, heuristic
#   infra/repair-summaries.sh --mode all        both, in one pass
#
# Dry run by default; add --apply to write. Try one blog first:
#
#   infra/repair-summaries.sh --mode all --filter "sourceId eq 'maxleiter'"
#
# Safe to re-run: a repaired summary no longer changes under the transform, so a second
# pass finds nothing. Safe to interrupt: re-run with --resume to skip finished slices.

set -euo pipefail

RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
SEARCH_SERVICE="${SEARCH_SERVICE:-srch-blogme-basic-b3d38b}"
ROLLBACK="${ROLLBACK:-summary-repair-rollback.jsonl.gz}"
CHECKPOINT="${CHECKPOINT:-summary-repair-checkpoint.json}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }

[[ -d infra ]] || die "run from the repository root"

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

log "Repair"
exec "$PYTHON" "$(dirname "$0")/repair_summaries.py" \
	--rollback "$ROLLBACK" --checkpoint "$CHECKPOINT" "$@"
