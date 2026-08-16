# Tech Stack

> Companion to [system-design.md](system-design.md). That document decides *what* we build and *which
> Azure services* run it. This one decides *what we write it in* and *how we work on it day to day*.

Validated against official documentation and package registries on **16 August 2026**.

## Summary

| Layer | Choice |
| --- | --- |
| Backend language | Go 1.26 |
| Backend host | Azure Functions, Flex Consumption, Linux |
| Functions programming model | First-class Go worker (`azure-functions-golang-worker`) |
| Frontend | SvelteKit + Svelte 5 + Tailwind CSS 4, `adapter-static` |
| UI components | Flowbite Svelte (stable 1.x) |
| Frontend host | GitHub Pages |
| Canonical storage | Azure Blob Storage |
| Search | Azure AI Search |
| Infrastructure as code | Bicep, applied with Azure CLI |
| Task runner | GNU Make |
| Local emulation | Azurite (blob), Azure AI Search Free tier |
| CI/CD | GitHub Actions |

## Backend: Go on Azure Functions

Go is supported as a **first-class language** on Azure Functions. Functions are registered in code;
there are no `function.json` files.

```go
app := sdk.FunctionApp()
app.HTTP("search", searchHandler, sdk.WithMethods("GET"), sdk.WithAuth("anonymous"))
app.Timer("discover", discoverHandler, sdk.WithSchedule("0 0 */6 * * *"))
worker.Start(app)
```

Core Tools detects Go from the presence of `go.mod`, compiles the project with `go build -o bin/app .`,
and talks to the resulting binary over gRPC. Therefore **`go.mod`, `main.go`, and `host.json` all live in
the same directory** — the function app root is the Go module root.

`FUNCTIONS_WORKER_RUNTIME` is `native`.

### Why not custom handlers

Custom handlers were the historical way to run Go on Functions. Microsoft's documentation now states
directly that new Go apps should use first-class Go support instead. Custom handlers remain the
fallback if the preview stalls.

### Preview status and risk

Go support is in **public preview**. Constraints that apply to us:

- Go function apps run **only on the Flex Consumption plan** — which is what the system design already
  specifies, so this costs us nothing.
- Linux only in Azure.
- Only a fixed set of triggers is supported. **HTTP and Timer are both supported**, which is the entire
  surface this project needs.
- Durable Functions is unsupported. We do not use it.
- `func new` is unsupported; functions are added by editing `main.go`.
- The worker SDK is at a `v0.x-preview` version.

**Mitigation:** `main.go` stays thin — configuration wiring and trigger registration only. All behaviour
lives under `internal/`. If the preview disappoints, replacing the entrypoint with a custom handler (or
another host entirely) does not touch the domain code.

### Requirements

| Tool | Minimum |
| --- | --- |
| Go | 1.24 (we run 1.26.1) |
| Azure Functions Core Tools | 4.12 |
| Azure CLI | 2.87.0 |

## Frontend: SvelteKit

Scaffolded with the official Svelte CLI (`npx sv create`) so that the SvelteKit, Vite, Svelte,
TypeScript, and Tailwind versions are a set the toolchain has actually validated together. We do not
hand-pin these.

`adapter-static` produces a fully static build, which is what GitHub Pages serves. The site is UI only:
it calls the Functions HTTP API and holds no Azure credentials.

### UI components

[Flowbite Svelte](https://flowbite-svelte.com) supplies the component set, on top of Tailwind. We use
the **stable 1.x** release, whose peer dependencies (`svelte ^5.40`, `tailwindcss ^4.1.4`) match this
project exactly.

We are deliberately **not** on `flowbite-svelte@2.0.0-next`. Version 2 is still a prerelease — its docs
site is a preview and it ships a `theme-selector` theming system that stable does not have. A component
library is a foundation, so it stays on a released version until v2 is stable.

Tailwind is configured in [web/src/routes/layout.css](../web/src/routes/layout.css): the Flowbite plugin,
a `dark` custom variant, the `primary`/`secondary` theme colours the components expect, and `@source`
directives so Tailwind scans the Flowbite package for the utility classes its components emit. That scan
is why the stylesheet is larger than a hand-written one — roughly 34 KB gzipped.

In development, Vite proxies `/api/*` to `http://localhost:7071` so the browser sees one origin and CORS
stays out of the inner loop. In production the API base URL is injected at build time and the Function
App is configured to allow the Pages origin.

## Data and search

**Azure Blob Storage** holds canonical article JSON and is the source of truth.

**Azure AI Search** holds the searchable projection, and is treated as rebuildable from Blob at any time.

We start on the **Free tier** while building: no cost, and enough for a development corpus. Its limits are
real — 50 MB, one free service per subscription, shared tenancy, no managed identity, no IP firewall, and
the service can be reclaimed after prolonged inactivity. We move to **Dedicated Basic** when the corpus
outgrows it or when we need managed identity, per the system design.

We do not use the Serverless Developer tier. As of this date it is preview, available in three regions,
carries no SLA, and begins billing on 13 September 2026.

## Local development

Azurite emulates Blob Storage locally, so the same Azure SDK code path runs in development and in Azure —
no filesystem stand-in, no second implementation.

Azure AI Search has **no emulator**. The Free-tier service is the development search backend, addressed
with an API key locally and a managed identity in Azure.

Because both dependencies are reachable with their real SDKs, `store` and `index` are plain concrete
clients. We are deliberately not introducing interfaces for them until a second implementation exists.

```bash
make dev     # azurite + func start (api) + vite dev (web)
make check   # golangci-lint, go test ./..., svelte-check, prettier
make build   # func pack (linux/amd64) + static web build
```

## Infrastructure

A single `infra/main.bicep` provisions the storage account, search service, Flex Consumption plan,
function app, and the managed identity role assignments. Applied with `az deployment group create`.

We are not using `azd`. Bicep plus two Azure CLI commands is fewer moving parts, and `azd` adds a tool
and a config layer we do not currently need.

## Deliberate omissions

Recorded so they are re-decided consciously rather than drifted into:

- **No pnpm/npm workspace.** There is one JavaScript package. A workspace is added when a second appears.
- **No port/adapter indirection** over storage or search. One implementation each.
- **No `azd`.**
- **No Docker Compose.** Azurite is the only local service and Make starts it.
- **No Bleve or other local search engine.** The Free tier replaces it.
- **No monorepo tooling** (Nx, Turborepo). Two applications and a Makefile.

## References

- [Go developer reference for Azure Functions](https://learn.microsoft.com/en-us/azure/azure-functions/functions-reference-go)
- [Azure Functions custom handlers](https://learn.microsoft.com/en-us/azure/azure-functions/functions-custom-handlers)
- [Azure Functions Flex Consumption plan](https://learn.microsoft.com/en-us/azure/azure-functions/flex-consumption-plan)
- [Choose a pricing model and service tier — Azure AI Search](https://learn.microsoft.com/en-us/azure/search/search-sku-tier)
- [Azurite emulator](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azurite)
- [SvelteKit static site generation](https://svelte.dev/docs/kit/adapter-static)
- [Tailwind CSS](https://tailwindcss.com/docs)
