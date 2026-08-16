#!/usr/bin/env bash
# Publishes the generated source list to blob storage.
#
# The discovery job reads this blob on its next run, so no redeploy is needed. A bad
# upload breaks discovery in production, hence the sanity checks before writing.

set -euo pipefail

RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
FUNCTION_APP="${FUNCTION_APP:-func-blogme-b3d38b}"
SOURCES_CONTAINER="${SOURCES_CONTAINER:-sources}"
SOURCES_FILE="${SOURCES_FILE:-sources/blogs.yml}"
BLOB_NAME="${BLOB_NAME:-blogs.yml}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }

log "Checking the local file"
[[ -f "$SOURCES_FILE" ]] || die "$SOURCES_FILE not found (run 'make sources' first)"

entries="$(grep -c '^  - id:' "$SOURCES_FILE" || true)"
[[ "$entries" -gt 0 ]] || die "$SOURCES_FILE contains no sources; refusing to upload"
grep -q '^sources:' "$SOURCES_FILE" || die "$SOURCES_FILE has no top-level 'sources:' key"

printf '  %s: %s entries, %s bytes\n' \
	"$SOURCES_FILE" "$entries" "$(wc -c <"$SOURCES_FILE")"

log "Resolving the storage account"
STORAGE_ACCOUNT="${STORAGE_ACCOUNT:-$(az functionapp config appsettings list \
	--name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" \
	--query "[?name=='BLOGME_STORAGE_ACCOUNT'].value | [0]" -o tsv)}"
[[ -n "$STORAGE_ACCOUNT" ]] || die "could not resolve BLOGME_STORAGE_ACCOUNT from $FUNCTION_APP"
echo "  $STORAGE_ACCOUNT"

log "Current blob"
current="$(az storage blob show \
	--account-name "$STORAGE_ACCOUNT" --container-name "$SOURCES_CONTAINER" --name "$BLOB_NAME" \
	--auth-mode login --query "properties.contentLength" -o tsv 2>/dev/null || echo "")"
echo "  ${current:-<none>} bytes"

log "Uploading"
az storage blob upload \
	--account-name "$STORAGE_ACCOUNT" \
	--container-name "$SOURCES_CONTAINER" \
	--name "$BLOB_NAME" \
	--file "$SOURCES_FILE" \
	--auth-mode login \
	--overwrite \
	--no-progress \
	--output none

uploaded="$(az storage blob show \
	--account-name "$STORAGE_ACCOUNT" --container-name "$SOURCES_CONTAINER" --name "$BLOB_NAME" \
	--auth-mode login --query "properties.contentLength" -o tsv)"

[[ "$uploaded" == "$(wc -c <"$SOURCES_FILE")" ]] \
	|| die "size mismatch after upload: blob $uploaded bytes, local $(wc -c <"$SOURCES_FILE") bytes"

log "Done"
cat <<EOF
  ${STORAGE_ACCOUNT}/${SOURCES_CONTAINER}/${BLOB_NAME}
  $uploaded bytes, $entries sources

  The discovery job picks this up on its next run; no redeploy needed.
EOF
