#!/usr/bin/env bash
# Creates or updates the Azure AI Search index from infra/search-index.json.
#
# The index is a rebuildable projection of the canonical article JSON in blob storage,
# so recreating it is always safe. Safe to re-run: PUT is an upsert.

set -euo pipefail

RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
SEARCH_SERVICE="${SEARCH_SERVICE:-srch-blogme-b3d38b}"
SCHEMA="${SCHEMA:-infra/search-index.json}"
API_VERSION="${API_VERSION:-2024-07-01}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }

[[ -f "$SCHEMA" ]] || die "$SCHEMA not found"
INDEX_NAME="$(python3 -c "import json,sys; print(json.load(open('$SCHEMA'))['name'])")"

log "Index"
echo "  service: $SEARCH_SERVICE"
echo "  index:   $INDEX_NAME"

ADMIN_KEY="$(az search admin-key show \
	--service-name "$SEARCH_SERVICE" --resource-group "$RESOURCE_GROUP" \
	--query primaryKey -o tsv)"
ENDPOINT="https://${SEARCH_SERVICE}.search.windows.net"

log "Applying schema"
response="$(curl -s -w '\n%{http_code}' -X PUT \
	"${ENDPOINT}/indexes/${INDEX_NAME}?api-version=${API_VERSION}" \
	-H "Content-Type: application/json" \
	-H "api-key: ${ADMIN_KEY}" \
	--data-binary "@${SCHEMA}")"

status="$(tail -n1 <<<"$response")"
body="$(sed '$d' <<<"$response")"

case "$status" in
201) echo "created" ;;
204 | 200) echo "updated" ;;
*)
	echo "$body" | head -20
	die "index PUT returned HTTP $status"
	;;
esac

log "Verifying"
curl -s "${ENDPOINT}/indexes/${INDEX_NAME}?api-version=${API_VERSION}" \
	-H "api-key: ${ADMIN_KEY}" |
	python3 -c "
import json,sys
d = json.load(sys.stdin)
print('  fields:', ', '.join(f['name'] for f in d['fields']))
print('  scoring profile:', d.get('defaultScoringProfile', '<none>'))
"

count="$(curl -s "${ENDPOINT}/indexes/${INDEX_NAME}/docs/\$count?api-version=${API_VERSION}" \
	-H "api-key: ${ADMIN_KEY}")"
echo "  documents: ${count}"
