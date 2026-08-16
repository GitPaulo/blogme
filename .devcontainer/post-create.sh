#!/usr/bin/env bash
# Installs the tools the devcontainer features do not provide.
set -euo pipefail

# Azure Functions Core Tools 4.12+ is required for the Go worker; Azurite emulates Blob Storage.
npm install -g "azure-functions-core-tools@4" azurite

corepack enable
corepack prepare pnpm@10.32.1 --activate

# Pinned to the version CI runs, so lint results match locally.
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2

make setup
