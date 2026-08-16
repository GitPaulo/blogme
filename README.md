# blogme
A search engine to find human written blogs about all kinds of things!

## Dev

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
