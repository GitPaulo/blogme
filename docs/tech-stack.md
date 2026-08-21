# Tech Stack

> Companion to [system-design.md](system-design.md). That document decides _what_ we build and _which
> Azure services_ run it. This one decides _what we write it in_ and _how we work on it day to day_.

Validated against official documentation and package registries on **16 August 2026**.

## Summary

| Layer                       | Choice                                                  |
| --------------------------- | ------------------------------------------------------- |
| Backend language            | Go 1.26                                                 |
| Backend host                | Azure Functions, Flex Consumption, Linux                |
| Functions programming model | First-class Go worker (`azure-functions-golang-worker`) |
| Frontend                    | SvelteKit + Svelte 5 + Tailwind CSS 4, `adapter-static` |
| UI components               | Flowbite Svelte (stable 1.x)                            |
| Frontend host               | GitHub Pages                                            |
| Canonical storage           | Azure Blob Storage                                      |
| Search                      | Azure AI Search                                         |
| Provisioning                | Re-runnable Azure CLI scripts in `infra/`               |
| Task runner                 | GNU Make                                                |
| Local emulation             | Azurite (blob); search has no emulator                  |
| CI/CD                       | GitHub Actions                                          |

## Backend: Go on Azure Functions

Go is supported as a **first-class language** on Azure Functions. Functions are registered in code;
there are no `function.json` files.

```go
app := sdk.FunctionApp()
app.HTTP("search", searchHandler, sdk.WithMethods("GET"), sdk.WithAuth("anonymous"))
app.Timer("discover", discoverHandler, sdk.WithSchedule(cfg.discoverySchedule))
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

| Tool                       | Minimum              |
| -------------------------- | -------------------- |
| Go                         | 1.24 (we run 1.26.1) |
| Azure Functions Core Tools | 4.12                 |
| Azure CLI                  | 2.87.0               |

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
Keyword scoring is the default; a query can opt into the **semantic ranker**, which reranks the top
keyword matches with a language model. It needs no embeddings, no re-indexing and no extra pipeline,
which is why it comes before vector search rather than after it. It is billed separately from the tier
and starts on the free plan (1,000 queries a month); `BLOGME_SEARCH_SEMANTIC_CONFIG=""` turns it off
without a redeploy.

We run on **Dedicated Basic**. The Free tier carried the early build, but its 50 MB ceiling is far
below what a corpus of this size needs, and it supports no managed identity. Azure cannot upgrade a
Free service in place, so Basic was provisioned as a separate service and the index rebuilt into it —
cheap to do precisely because the index is a projection of blob storage rather than a source of truth.

We do not use the Serverless Developer tier. As of this date it is preview, available in three regions,
carries no SLA, and begins billing on 13 September 2026.

## Local development

Azurite emulates Blob Storage locally, so the same Azure SDK code path runs in development and in Azure —
no filesystem stand-in, no second implementation.

Azure AI Search has **no emulator**, so development runs against a real service, addressed with an API
key locally and a managed identity in Azure.

Because both dependencies are reachable with their real SDKs, `store` and `index` are plain concrete
clients. We are deliberately not introducing interfaces for them until a second implementation exists.

```bash
make dev     # azurite + func start (api) + vite dev (web)
make check   # golangci-lint, go test ./..., svelte-check, prettier
make build   # func pack (linux/amd64) + static web build
```

## Infrastructure

Four bash scripts under [`infra/`](../infra/), each safe to re-run because every step checks for the
resource before creating it:

| Script                   | What it does                                                                           |
| ------------------------ | -------------------------------------------------------------------------------------- |
| `provision.sh`           | Storage account, search service, Flex Consumption function app, role assignments, CORS |
| `create-search-index.sh` | Applies [`search-index.json`](../infra/search-index.json) to the search service        |
| `github-oidc.sh`         | The Entra ID app and federated credential the deploy workflows authenticate with       |
| `upload-sources.sh`      | Publishes `blogs.yml` to blob storage                                                  |

One setting the scripts apply that is easy to miss, because it looks like a default and
is not: `httpsOnly` — a Function App answers plain HTTP until told otherwise.
`http20Enabled` is deliberately **off**, required by the Go preview. There is no
`healthCheckPath`: the platform would ping it every minute, which on Flex Consumption
keeps an instance warm and defeats scale-to-zero for a workload that is idle between
hourly runs. Alerting is set up separately; see
[discovery-cadence.md](discovery-cadence.md#alerting).

`maximumInstanceCount` is pinned to **10**, down from the default of 100, and it is the
only hard limit on what the app can cost. Flex Consumption bills each instance for its
memory for as long as it is up, so a saturated pool of 100 runs to roughly $640 a day —
against a bill that is otherwise around $86 a month. Ten is five times the busiest hour
ever recorded and well past what the Basic search tier behind it can answer, so it bounds
the loss without bounding real traffic. `blogme-instances-scaling-out` (sev 2) says when
more than five are running, which normal operation has never needed; the observed peak is
two. A budget alert cannot do that job, because budgets evaluate every 8–24 hours and
notify rather than cap.

Not Bicep, and not `azd`. Go on Functions is in public preview and only its Azure CLI path is
documented, so a declarative template would have to be reverse-engineered from CLI behaviour that is
still moving. Idempotent scripts follow the documentation directly and stay readable. Revisit when
the preview settles.

## Deliberate omissions

Recorded so they are re-decided consciously rather than drifted into:

- **No pnpm/npm workspace.** There is one JavaScript package. A workspace is added when a second appears.
- **No port/adapter indirection** over storage or search. One implementation each.
- **No `azd`, and no Bicep.** See [Infrastructure](#infrastructure).
- **No Docker Compose.** Azurite is the only local service and Make starts it.
- **No Bleve or other local search engine.** Development points at the real search service.
- **No monorepo tooling** (Nx, Turborepo). Two applications and a Makefile.

## References

- [Go developer reference for Azure Functions](https://learn.microsoft.com/en-us/azure/azure-functions/functions-reference-go)
- [Azure Functions custom handlers](https://learn.microsoft.com/en-us/azure/azure-functions/functions-custom-handlers)
- [Azure Functions Flex Consumption plan](https://learn.microsoft.com/en-us/azure/azure-functions/flex-consumption-plan)
- [Choose a pricing model and service tier — Azure AI Search](https://learn.microsoft.com/en-us/azure/search/search-sku-tier)
- [Azurite emulator](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azurite)
- [SvelteKit static site generation](https://svelte.dev/docs/kit/adapter-static)
- [Tailwind CSS](https://tailwindcss.com/docs)
