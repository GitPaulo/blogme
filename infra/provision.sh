#!/usr/bin/env bash
# Provisions the Azure resources described in docs/system-design.md.
#
# Safe to re-run: every step checks for an existing resource first.
#
# Go on Azure Functions is in public preview and only runs on Flex Consumption,
# so this follows the documented Azure CLI path rather than hand-written Bicep.
# See docs/tech-stack.md.

set -euo pipefail

LOCATION="${LOCATION:-uksouth}"
RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"

# Storage, search and function app names must be globally unique. Derive a stable
# suffix from the subscription so re-runs resolve to the same names.
SUBSCRIPTION_ID="$(az account show --query id -o tsv)"
SUFFIX="${SUFFIX:-${SUBSCRIPTION_ID//-/}}"
SUFFIX="${SUFFIX:0:6}"

STORAGE_ACCOUNT="${STORAGE_ACCOUNT:-stblogme${SUFFIX}}"
# Free tier caps at 50 MB, which the corpus exceeds by ~50x. Azure cannot upgrade a
# Free service in place, so Basic runs as a separate service.
SEARCH_SERVICE="${SEARCH_SERVICE:-srch-blogme-basic-${SUFFIX}}"
FUNCTION_APP="${FUNCTION_APP:-func-blogme-${SUFFIX}}"
SEARCH_SKU="${SEARCH_SKU:-basic}"
ARTICLES_CONTAINER="${ARTICLES_CONTAINER:-articles}"
SOURCES_CONTAINER="${SOURCES_CONTAINER:-sources}"
SEARCH_INDEX="${SEARCH_INDEX:-articles}"
# Origins the static site is served from, space separated. No trailing slashes: an
# origin is scheme + host + port only. Development goes through the Vite proxy, so only
# deployed origins need allowing.
WEB_ORIGINS="${WEB_ORIGINS:-https://gitpaulo.github.io https://gitpaulo.moe}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }

log "Plan"
cat <<EOF
  Subscription    $(az account show --query name -o tsv) ($SUBSCRIPTION_ID)
  Location        $LOCATION
  Resource group  $RESOURCE_GROUP
  Storage         $STORAGE_ACCOUNT
  Search          $SEARCH_SERVICE (sku: $SEARCH_SKU)
  Function app    $FUNCTION_APP
EOF

log "Resource group"
az group create --name "$RESOURCE_GROUP" --location "$LOCATION" --output none
echo "ok: $RESOURCE_GROUP"

log "Storage account"
if az storage account show --name "$STORAGE_ACCOUNT" --resource-group "$RESOURCE_GROUP" &>/dev/null; then
	echo "exists: $STORAGE_ACCOUNT"
else
	az storage account create \
		--name "$STORAGE_ACCOUNT" \
		--resource-group "$RESOURCE_GROUP" \
		--location "$LOCATION" \
		--sku Standard_LRS \
		--allow-blob-public-access false \
		--min-tls-version TLS1_2 \
		--output none
	echo "created: $STORAGE_ACCOUNT"
fi

log "Azure AI Search"
if az search service show --name "$SEARCH_SERVICE" --resource-group "$RESOURCE_GROUP" &>/dev/null; then
	echo "exists: $SEARCH_SERVICE"
else
	# aadOrApiKey keeps both Entra ID and key auth working, so the app can use a
	# managed identity while local development uses a key.
	az search service create \
		--name "$SEARCH_SERVICE" \
		--resource-group "$RESOURCE_GROUP" \
		--location "$LOCATION" \
		--sku "$SEARCH_SKU" \
		--auth-options aadOrApiKey \
		--aad-auth-failure-mode http401WithBearerChallenge \
		--output none
	echo "created: $SEARCH_SERVICE"
fi

log "Semantic ranker"
# Reranking is billed separately from the tier and is off until the service opts in.
# The free plan allows 1,000 queries a month, which is enough to judge whether the
# quality gain is worth the standard plan. Switch with SEMANTIC_PLAN=standard.
SEMANTIC_PLAN="${SEMANTIC_PLAN:-free}"
az search service update \
	--name "$SEARCH_SERVICE" \
	--resource-group "$RESOURCE_GROUP" \
	--semantic-search "$SEMANTIC_PLAN" \
	--output none
echo "ok: semantic-search=$SEMANTIC_PLAN"

log "Function app (Flex Consumption, Go)"
if az functionapp show --name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" &>/dev/null; then
	echo "exists: $FUNCTION_APP"
else
	az functionapp create \
		--name "$FUNCTION_APP" \
		--resource-group "$RESOURCE_GROUP" \
		--storage-account "$STORAGE_ACCOUNT" \
		--flexconsumption-location "$LOCATION" \
		--runtime go \
		--runtime-version 1.0 \
		--functions-version 4 \
		--assign-identity '[system]' \
		--output none
	echo "created: $FUNCTION_APP"
fi

log "Disable HTTP/2 (required during the Go public preview)"
az resource update \
	--resource-group "$RESOURCE_GROUP" \
	--resource-type Microsoft.Web/sites \
	--name "$FUNCTION_APP" \
	--set properties.siteConfig.http20Enabled=false \
	--output none
echo "ok: http20Enabled=false"

log "Transport"
# Without this the app answers plain HTTP as happily as HTTPS, so a query can be sent
# — and read — in the clear. Set here rather than by hand, because a re-provision that
# quietly reopens it looks exactly like a provision that worked.
az functionapp update \
	--name "$FUNCTION_APP" \
	--resource-group "$RESOURCE_GROUP" \
	--set httpsOnly=true \
	--output none
echo "ok: httpsOnly=true"

log "Scale ceiling"
# The only hard limit on what this app can cost. Flex Consumption defaults to 100
# instances and bills each one for its memory for as long as it is up, so a flood
# arriving while nobody is awake is a four-figure day. Ten is five times the busiest
# hour ever recorded here, and still far more than the Basic search tier behind it
# can answer — so this caps the bill without ever capping real traffic.
az functionapp scale config set \
	--name "$FUNCTION_APP" \
	--resource-group "$RESOURCE_GROUP" \
	--maximum-instance-count 10 \
	--output none
echo "ok: maximumInstanceCount=10"


log "Role assignments for the function app identity"
PRINCIPAL_ID="$(az functionapp identity show \
	--name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" --query principalId -o tsv)"
STORAGE_ID="$(az storage account show \
	--name "$STORAGE_ACCOUNT" --resource-group "$RESOURCE_GROUP" --query id -o tsv)"
SEARCH_ID="$(az search service show \
	--name "$SEARCH_SERVICE" --resource-group "$RESOURCE_GROUP" --query id -o tsv)"

assign_role() {
	local role="$1" scope="$2"
	if az role assignment list --assignee "$PRINCIPAL_ID" --scope "$scope" \
		--query "[?roleDefinitionName=='$role']" -o tsv | grep -q .; then
		echo "exists: $role"
	else
		az role assignment create \
			--assignee-object-id "$PRINCIPAL_ID" \
			--assignee-principal-type ServicePrincipal \
			--role "$role" \
			--scope "$scope" \
			--output none
		echo "assigned: $role"
	fi
}

assign_role "Storage Blob Data Contributor" "$STORAGE_ID"
assign_role "Search Index Data Contributor" "$SEARCH_ID"
assign_role "Search Service Contributor" "$SEARCH_ID"

log "Blob containers"
# Being subscription Owner does not grant data-plane access, so grant the signed-in
# user the data role needed to upload the source list.
USER_ID="$(az ad signed-in-user show --query id -o tsv)"
if az role assignment list --assignee "$USER_ID" --scope "$STORAGE_ID" \
	--query "[?roleDefinitionName=='Storage Blob Data Contributor']" -o tsv | grep -q .; then
	echo "exists: operator Storage Blob Data Contributor"
else
	az role assignment create \
		--assignee-object-id "$USER_ID" \
		--assignee-principal-type User \
		--role "Storage Blob Data Contributor" \
		--scope "$STORAGE_ID" \
		--output none
	echo "assigned: operator Storage Blob Data Contributor"
fi

for c in "$SOURCES_CONTAINER" "$ARTICLES_CONTAINER"; do
	az storage container create \
		--name "$c" \
		--account-name "$STORAGE_ACCOUNT" \
		--auth-mode login \
		--output none 2>/dev/null && echo "ok: $c" || echo "ok: $c (already exists)"
done

log "Application settings"
az functionapp config appsettings set \
	--name "$FUNCTION_APP" \
	--resource-group "$RESOURCE_GROUP" \
	--settings \
	"BLOGME_SEARCH_ENDPOINT=https://${SEARCH_SERVICE}.search.windows.net" \
	"BLOGME_SEARCH_INDEX=${SEARCH_INDEX}" \
	"BLOGME_SEARCH_SEMANTIC_CONFIG=blogme-semantic" \
	"BLOGME_ARTICLES_CONTAINER=${ARTICLES_CONTAINER}" \
	"BLOGME_SOURCES_CONTAINER=${SOURCES_CONTAINER}" \
	"BLOGME_SOURCES_BLOB=blogs.yml" \
	"BLOGME_STORAGE_ACCOUNT=${STORAGE_ACCOUNT}" \
	--output none
echo "ok"

log "CORS"
allowed="$(az functionapp cors show --name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" \
	--query "allowedOrigins" -o tsv 2>/dev/null || true)"
for origin in $WEB_ORIGINS; do
	if grep -qx "$origin" <<<"$allowed"; then
		echo "exists: $origin"
	else
		az functionapp cors add \
			--name "$FUNCTION_APP" \
			--resource-group "$RESOURCE_GROUP" \
			--allowed-origins "$origin" \
			--output none
		echo "allowed: $origin"
	fi
done

log "Done"
cat <<EOF
  API base URL   https://${FUNCTION_APP}.azurewebsites.net
  Health check   https://${FUNCTION_APP}.azurewebsites.net/api/health

  Upload sources:  make sources-upload
  Deploy with:     cd api && func azure functionapp publish ${FUNCTION_APP}
  Tear down:       az group delete --name ${RESOURCE_GROUP} --yes
EOF
