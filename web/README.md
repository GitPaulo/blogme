# web

The blogme search UI: a SvelteKit app built to static files and served from GitHub
Pages. It holds no credentials and talks only to the Functions HTTP API.

Run it from the repository root, so the API and the blob emulator come up with it:

```bash
make dev     # Azurite + Functions host + Vite dev server
make check   # svelte-check, prettier, vitest
make build   # static build into web/build
```

`pnpm dev`, `pnpm check`, `pnpm test` and `pnpm build` do the web half on their own. In
development Vite proxies `/api/*` to `http://localhost:7071`, so the browser sees one
origin; in production the API base URL is injected at build time.

| Path                 | Contents                                                 |
| -------------------- | -------------------------------------------------------- |
| `src/routes/`        | The single page, its layout and the Tailwind entry point |
| `src/lib/`           | API client, query parsing, filters, suggestions          |
| `src/lib/bookmarks`  | Saved articles, kept in IndexedDB                        |
| `src/lib/visited`    | Which results have been opened, kept in IndexedDB        |
| `src/lib/components` | Search suggestions, filter bar, link preview, bookmarks  |

See [how it works](../docs/how-it-works.md) for the read path this renders, and
[tech stack](../docs/tech-stack.md) for why SvelteKit, Tailwind and Flowbite.

## Recreating the scaffolding

```sh
pnpm dlx sv@0.17.0 create --template minimal --types ts \
  --add tailwindcss="plugins:none" sveltekit-adapter="adapter:static" \
  prettier --no-download-check --install pnpm web
```
