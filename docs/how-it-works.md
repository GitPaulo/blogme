# How It Works

An end-to-end walkthrough of blogme, from a list of blogs to a search result.

For the reasoning behind these choices see [system design](system-design.md),
[tech stack](tech-stack.md) and the [high-level plan](blog-discovery-search-high-level-plan.md).

## Overview

There are two independent paths through the system. The **write path** fills the corpus
on a timer; the **read path** answers a user's query. They meet only at the search index.

```mermaid
flowchart LR
    subgraph Git["Repository"]
        Y["blogs.yml<br/>47,102 blogs"]
    end

    subgraph Azure["Azure"]
        B["Blob Storage<br/>sources + articles"]
        D["discover<br/>timer function"]
        S["AI Search<br/>index"]
        A["search<br/>HTTP function"]
    end

    subgraph Web["GitHub Pages"]
        U["Browser"]
    end

    Y -->|make sources-upload| B
    B -->|read list| D
    D -->|feeds and sitemaps| Blogs["The blogs<br/>themselves"]
    D -->|article JSON| B
    D -->|documents| S
    U -->|/api/search| A
    A -->|query| S
```

The two paths are deliberately decoupled: discovery can fail entirely and search keeps
working against whatever is already indexed.

## Write path — filling the corpus

Runs on a timer. Each pass handles a slice of the source list and records where it
stopped, so no single run approaches the function timeout.

```mermaid
flowchart TD
    T["Timer fires"] --> L["Load blogs.yml from blob<br/>ETag-cached"]
    L --> C["Read cursor<br/>resume after last source"]
    C --> BATCH["Take next 1,000 blogs"]

    BATCH --> R{"robots.txt<br/>allows the fetch?"}
    R -->|no| SKIP["Skip"]
    R -->|yes| P{"Has a feed?"}

    P -->|"yes — 38,956 blogs"| F["Fetch RSS/Atom feed"]
    P -->|"no — 8,146 blogs"| M["Find sitemap<br/>robots.txt, then common paths"]

    F --> ITEMS["Parse entries<br/>title, link, date, content"]
    ITEMS --> FULL{"Feed content<br/>200+ words?"}
    FULL -->|yes| X["Extract text"]
    FULL -->|no| PAGE["Fetch the post page"] --> X

    M -->|"no sitemap"| ALT["Read the homepage<br/>for a declared feed"]
    ALT --> F
    M --> LINKS["Article-shaped URLs,<br/>newest first, skip stored"]
    LINKS --> FETCH["Fetch the page"]
    FETCH --> LONG{"250+ words?"}
    LONG -->|no| SKIP
    LONG -->|yes| X

    X --> TRUNC["Clean and truncate<br/>1,000 words"]
    TRUNC --> SAVE["Save article JSON<br/>to blob"]
    TRUNC --> IDX["Upsert to search index<br/>batches of 1,000"]
    SAVE --> CUR["Write cursor"]
    IDX --> CUR
```

A feed describes its own posts, so it is both cheaper and more accurate: one request
usually yields every recent post, with a title, a link and a date already attached. The
sitemap path exists for the third of the corpus that publishes no feed. It is slower by
design — a sitemap lists every page a site has, so each candidate must be fetched before
it can be judged, and the word count is what separates a post from a landing page. Which
path found an article is recorded on it as its **origin**, because sitemap metadata is
the less dependable of the two and the UI says so.

A blog with neither is read by neither path, so before giving up the crawler reads the
homepage and uses a feed the page advertises. That is a repair, not a route: it means
the source list is missing a feed the blog has been publishing all along, and the
durable fix is [`blogs-overrides.yml`](../sources/README.md#corrections-by-hand).

Key properties:

| Property        | How                                                                              |
| --------------- | -------------------------------------------------------------------------------- |
| Bounded runtime | Fixed batch of blogs per run, never the whole list                               |
| Resumable       | Cursor stores the last source **ID**, so it survives list regeneration           |
| Polite          | robots.txt respected; concurrency capped per registrable domain                  |
| Idempotent      | Article IDs are a hash of the URL, so re-crawling updates rather than duplicates |
| Incremental     | Sitemap pages already stored are skipped, so later runs reach deeper             |
| Fault isolated  | One failing blog is logged and skipped; the pass continues                       |

The per-domain cap matters more than it looks: shared platforms host thousands of the
sources, with `bearblog.dev` alone accounting for over a thousand. Limiting by hostname
would not help, because each blog is its own subdomain of the same server.

## Read path — answering a query

```mermaid
sequenceDiagram
    participant U as Browser
    participant P as GitHub Pages
    participant F as Functions (Go)
    participant S as AI Search

    U->>P: load gitpaulo.moe/blogme
    P-->>U: static HTML, JS, CSS
    U->>F: GET /api/search?q=...
    Note over F: validate q, limit, offset, origin
    F->>S: full-text query, ranked
    S-->>F: matching documents
    F-->>U: JSON results
    Note over U: render result cards
```

The site is static and holds no credentials. The function app authenticates to Azure
with a managed identity, so no keys exist in the browser or in the repository.

Every query parameter is validated before it reaches the index, and the one filter the
API offers — `origin`, which narrows results to feed or sitemap discoveries — is built
from a fixed set of expressions rather than from the caller's string, so no filter can be
injected through the query.

The endpoint is anonymous, so there is no key to revoke and a **rate limit** is what
stands between a script and the bill. Callers are identified by the address Azure appends
to `X-Forwarded-For`, and a throttled request is answered with `429`, `Retry-After` and
the `RateLimit-*` headers. Semantic queries carry a second, tighter allowance — per caller
and across the service — because reranking spends from a metered monthly quota rather than
from capacity that renews by the minute.

| Setting                           | Default | Applies to             |
| --------------------------------- | ------- | ---------------------- |
| `BLOGME_SEARCH_RATE_PER_MINUTE`   | 60      | One caller, any search |
| `BLOGME_SEARCH_RATE_BURST`        | 30      | One caller, any search |
| `BLOGME_SEMANTIC_RATE_PER_MINUTE` | 10      | One caller, semantic   |
| `BLOGME_SEMANTIC_RATE_BURST`      | 5       | One caller, semantic   |
| `BLOGME_SEMANTIC_RATE_PER_HOUR`   | 60      | Everyone, semantic     |
| `BLOGME_SEMANTIC_RATE_HOUR_BURST` | 15      | Everyone, semantic     |

These are per instance, and Flex Consumption scales out, so they bound the blast radius
rather than enforce a budget: they turn "burn the month's reranking in a minute" into
"burn it over many hours", which is long enough to notice. A global cap is what a
budget would need, and only a paid semantic plan makes one worth setting.

A page also carries at most three results from any one blog. Three posts from one site is
rarely what a reader wanted, and it means a source that stuffs its posts with popular
terms takes three rows rather than the whole page.

Ranking happens in two stages. Keyword scoring picks the candidates, weighted towards the
title, and then Azure AI Search's **semantic ranker** reorders them with a language model
— which is what makes a query phrased as a sentence work rather than only a bag of
keywords. The reranker only reaches the top 50 keyword matches, so that window is also
the entire result set the API offers: past it, ordering would quietly revert to keyword
scoring part-way down a scroll. Reranking is metered, so a query is downgraded to keyword
ranking when the throttle says the budget is spent, and retried without it if the service
refuses anyway. Search degrades rather than failing, either way.

A search lives in the address bar. The query, the ranking mode and the `origin` filter —
everything the server was asked for — are written back as `?q=`, `?mode=` and `?origin=`,
so a search can be shared, reloaded or returned to. The remaining filters narrow the rows
already fetched rather than the query behind them, and a fresh search clears them, so they
stay out. The URL is written once per search rather than once per keystroke: partly
because it should describe results that exist, and partly because browsers throttle
history writes.

An answer is cacheable for a minute, so a reload or a shared link opened twice costs
neither an execution nor an index query. Only the answer: an error describes this moment
rather than the query, and caching one would go on serving a failure the service had
already recovered from.

`/api/health` asks the index for a document count rather than reporting that the process
is up. The deploy workflow gates on it, and the failures worth catching all authenticate
correctly — a role assignment that was never granted still issues a token, and a
misspelled index name is a valid request to somewhere that is not there. Both would ship
green and then fail every search. Counting is not a semantic query, so the check spends
nothing from the reranking quota.

## Logging

There is no logger to configure. The Functions Go worker installs one at import time:
its slog handler attaches `invocation_id`, `function_name` and `trigger_type` to every
record, and once the worker connects it sends them to the host over gRPC, which is what
puts them in Application Insights. HTTP handlers get this for free, because the
dispatcher hands the handler a request whose context carries the invocation.

Two rules follow, and the first one matters more than it looks:

- **Never call `slog.SetDefault`.** Installing a handler of our own replaces the SDK's,
  and records fall back to stderr without `invocation_id`. Nothing fails loudly; the logs
  simply stop being correlated.
  [`logging_test.go`](../api/internal/httpapi/logging_test.go) pins this.
- **Always use the `*Context` variants** — `slog.InfoContext(ctx, …)` — inside a request
  or a timer run. The plain `slog.Info` is right only at startup, where no invocation
  exists yet.

Levels carry a meaning worth keeping to:

| Level   | Means                                                           |
| ------- | --------------------------------------------------------------- |
| `Error` | We are broken and someone must look                             |
| `Warn`  | One input was bad, or a caller was refused; the run continued   |
| `Info`  | One line per unit of work — a search, a discovery pass, startup |
| `Debug` | Per-item detail, off by default                                 |

Field names are shared across packages so a query works everywhere: `duration_ms`,
`source_id`, `url`, `error`, `count`, `total`, `rank`, `kind`, `caller`.

Every search emits exactly one `search` record with the query, `count`, `total`, `rank`
and `duration_ms`. That one line is the difference between knowing search works and
assuming it: a corpus that has quietly stopped matching anything otherwise looks
identical to a quiet day. Queries are logged, capped at 128 characters and with control
characters folded to spaces so nothing in a query can forge a second log record. A
passing health check is deliberately not logged — it is polled, and one line per poll
would bury everything else — but a failing one is, because by then something is wrong
that nobody has noticed.

Turning on debug needs no redeploy. Add the app setting and restart:

```bash
az functionapp config appsettings set \
  --name <FUNCTION_APP> --resource-group <RESOURCE_GROUP> \
  --settings AzureFunctionsJobHost__logging__logLevel__default=Debug
```

The host pushes that threshold to the worker, which filters by category before anything
crosses the wire. Set it back to `Information` when finished — at `Debug` the discovery
job logs a line per source, which at 1,000 sources an hour is not something to leave on.

## Where each stage lives

| Stage                   | Code                                                                            |
| ----------------------- | ------------------------------------------------------------------------------- |
| Build the blog list     | [`sources/tools/`](../sources/tools/)                                           |
| Publish the list        | [`infra/upload-sources.sh`](../infra/upload-sources.sh)                         |
| Load and cache the list | [`api/internal/sources`](../api/internal/sources)                               |
| Batching and cursor     | [`api/internal/discovery/discovery.go`](../api/internal/discovery/discovery.go) |
| Feeds, fetching, robots | [`api/internal/discovery`](../api/internal/discovery)                           |
| Sitemap fallback        | [`api/internal/discovery/sitemap.go`](../api/internal/discovery/sitemap.go)     |
| Text extraction         | [`api/internal/discovery/extract.go`](../api/internal/discovery/extract.go)     |
| Canonical storage       | [`api/internal/store`](../api/internal/store)                                   |
| Index and query         | [`api/internal/index`](../api/internal/index)                                   |
| HTTP handlers           | [`api/internal/httpapi`](../api/internal/httpapi)                               |
| Web UI                  | [`web/src`](../web/src)                                                         |

## Data at each stage

One line of `blogs.yml` becomes many search results:

```mermaid
flowchart LR
    A["Source<br/>id, site, feed, kind, tags"] -->|crawl| B["Article<br/>title, author, date, origin,<br/>summary, content, topics, kind"]
    B -->|project| C["Result<br/>title, author, date, origin,<br/>summary, topics, score"]
```

A source's `tags` describe the blog, so on their own every post from it would be labelled
identically. On the feed path the post's own categories are added to them, which is the
only per-post subject signal available without reading the post. `kind` travels
unchanged: it is a fact about the blog, not about any one article.

Blob storage holds the canonical `Article`. The search index is a **projection** of it and
is treated as disposable: it can be dropped and rebuilt from blob at any time, which is
why moving between search tiers is cheap.

## Deployment

```mermaid
flowchart LR
    PUSH["push to main"] --> CI["ci.yml<br/>lint, test, build"]
    PUSH --> W{"changed paths"}
    W -->|"api/**"| DA["deploy-api.yml<br/>→ Azure Functions"]
    W -->|"web/**"| DW["deploy-pages.yml<br/>→ GitHub Pages"]
```

Both deploy workflows authenticate to Azure with OIDC federation, so no long-lived
credential is stored in GitHub. Publishing a new blog list is separate from deployment:
`make sources-upload` is enough, and the running job picks it up on its next pass.
