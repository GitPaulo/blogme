#!/usr/bin/env bash
# Creates the Entra ID identity that GitHub Actions uses to deploy.
#
# Uses OpenID Connect federation rather than a client secret: GitHub presents a
# short-lived token that Entra trusts for this specific repository and branch, so
# there is no long-lived credential to store in GitHub or rotate.
#
# Safe to re-run.

set -euo pipefail

APP_NAME="${APP_NAME:-blogme-github}"
REPO="${REPO:-GitPaulo/blogme}"
BRANCH="${BRANCH:-main}"
RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
ROLE="${ROLE:-Contributor}"

SUBSCRIPTION_ID="$(az account show --query id -o tsv)"
TENANT_ID="$(az account show --query tenantId -o tsv)"
SCOPE="/subscriptions/${SUBSCRIPTION_ID}/resourceGroups/${RESOURCE_GROUP}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }

log "App registration"
APP_ID="$(az ad app list --display-name "$APP_NAME" --query "[0].appId" -o tsv)"
if [[ -n "$APP_ID" ]]; then
	echo "exists: $APP_NAME ($APP_ID)"
else
	APP_ID="$(az ad app create --display-name "$APP_NAME" --query appId -o tsv)"
	echo "created: $APP_NAME ($APP_ID)"
fi

log "Service principal"
if az ad sp show --id "$APP_ID" &>/dev/null; then
	echo "exists"
else
	az ad sp create --id "$APP_ID" --output none
	echo "created"
fi
PRINCIPAL_ID="$(az ad sp show --id "$APP_ID" --query id -o tsv)"

log "Federated credential"
# GitHub now prefixes the subject claim with immutable numeric owner and repo IDs,
# so a rename cannot be used to impersonate the repository. Read the prefix from
# GitHub rather than assuming a format: the subject must match byte for byte or the
# token exchange fails with AADSTS700213.
SUB_PREFIX="$(gh api "repos/${REPO}/actions/oidc/customization/sub" \
	--jq '.sub_claim_prefix' 2>/dev/null || true)"
if [[ -z "$SUB_PREFIX" || "$SUB_PREFIX" == "null" ]]; then
	SUB_PREFIX="repo:${REPO}"
fi

SUBJECT="${SUB_PREFIX}:ref:refs/heads/${BRANCH}"
CRED_NAME="github-${BRANCH}"

EXISTING_SUBJECT="$(az ad app federated-credential list --id "$APP_ID" \
	--query "[?name=='$CRED_NAME'].subject | [0]" -o tsv)"

if [[ "$EXISTING_SUBJECT" == "$SUBJECT" ]]; then
	echo "exists: $SUBJECT"
else
	if [[ -n "$EXISTING_SUBJECT" ]]; then
		echo "replacing stale subject: $EXISTING_SUBJECT"
		az ad app federated-credential delete --id "$APP_ID" --federated-credential-id "$CRED_NAME" --output none
	fi
	az ad app federated-credential create --id "$APP_ID" --parameters "{
		\"name\": \"${CRED_NAME}\",
		\"issuer\": \"https://token.actions.githubusercontent.com\",
		\"subject\": \"${SUBJECT}\",
		\"audiences\": [\"api://AzureADTokenExchange\"]
	}" --output none
	echo "created: $SUBJECT"
fi

log "Role assignment"
if az role assignment list --assignee "$PRINCIPAL_ID" --scope "$SCOPE" \
	--query "[?roleDefinitionName=='$ROLE']" -o tsv | grep -q .; then
	echo "exists: $ROLE on $RESOURCE_GROUP"
else
	az role assignment create \
		--assignee-object-id "$PRINCIPAL_ID" \
		--assignee-principal-type ServicePrincipal \
		--role "$ROLE" \
		--scope "$SCOPE" \
		--output none
	echo "assigned: $ROLE on $RESOURCE_GROUP"
fi

log "Set these in GitHub"
cat <<EOF
  Settings > Secrets and variables > Actions > Secrets

    AZURE_CLIENT_ID        ${APP_ID}
    AZURE_TENANT_ID        ${TENANT_ID}
    AZURE_SUBSCRIPTION_ID  ${SUBSCRIPTION_ID}

  No client secret is needed: GitHub authenticates by OIDC federation.

  Revoke access at any time with:
    az ad app delete --id ${APP_ID}
EOF
