# blogme
A search engine to find human written blogs about all kinds of things!

## Documentation

- [High-level plan](docs/blog-discovery-search-high-level-plan.md) — what the product is for.
- [System design](docs/system-design.md) — the architecture and Azure services.
- [Tech stack](docs/tech-stack.md) — languages, tooling and the local development loop.

## Layout

| Path | Contents |
| --- | --- |
| `api/` | Azure Functions app (Go): search HTTP API and the discovery timer job |
| `web/` | SvelteKit static site, deployed to GitHub Pages |
| `sources/` | [Curated list of approved blogs](sources/README.md), plus the extractor that builds it |
| `docs/` | Design documentation |

## Getting started

The dev container installs everything. Then:

```bash
make setup   # install dependencies, create local config
make dev     # Azurite + Functions host + Vite dev server
```

- Web: <http://localhost:5173>
- API: <http://localhost:7071/api/search?q=test>

`/api/*` is proxied from the dev server to the Functions host, so there is no CORS setup locally.

```bash
make check   # lint, type-check and test everything
make build   # build both deployable artefacts
make help    # list all targets
```
