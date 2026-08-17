# Discovery Cadence

> How often discovery runs, how much it does per run, and how to change it.
> Companion to [system-design.md](system-design.md).

Sized against the generated source list on **17 August 2026**: 28,270 sources, 19,222 with a
feed and 9,048 without.

## Summary

| Setting                     | Deployed      | Code default    | Meaning                  |
| --------------------------- | ------------- | --------------- | ------------------------ |
| `BLOGME_DISCOVERY_SCHEDULE` | `0 0 * * * *` | `0 0 */6 * * *` | Timer cron, hourly       |
| `BLOGME_DISCOVERY_BATCH`    | `500`         | `200`           | Sources examined per run |

Both are Function App application settings, so changing cadence is a configuration change
and needs **no redeploy**. The deployed values override the code defaults in
[config.go](../api/config.go); the fallbacks apply only when a setting is absent.

## Why discovery is batched

One run cannot walk the whole list. The corpus is tens of thousands of sources, each
needing a robots.txt check, a feed or sitemap read, and a fetch per new post, while the
Functions host caps an invocation at **30 minutes**.

So each run takes a slice of the list and records the last source it handled. The next run
resumes after it and wraps around at the end, giving every source regular coverage without
any single run approaching the timeout.

```mermaid
flowchart LR
    A[Read source list<br/>from blob] --> B[Resume after<br/>last cursor]
    B --> C[Process N sources]
    C --> D[Save articles<br/>+ index in batches]
    D --> E[Write cursor]
    E -.->|next run| B
```

## Choosing a cadence

Coverage is simply how many sources a day the schedule gets through:

```text
sources/day = (24 / schedule_hours) x batch_size
full pass   = 28,270 / sources per day
```

| Batch   | Schedule   | Sources/day | Full pass    |
| ------- | ---------- | ----------- | ------------ |
| 200     | every 6h   | 800         | 35 days      |
| **500** | **hourly** | **12,000**  | **2.4 days** |
| 1,000   | hourly     | 24,000      | 1.2 days     |
| 2,000   | hourly     | 48,000      | 0.6 days     |

The code defaults are deliberately conservative and are **not** a recommendation for
production. At 35 days per pass a blog's new post could take a month to become searchable,
which defeats the goal in the [high-level plan](blog-discovery-search-high-level-plan.md)
that new posts appear automatically.

**Deployed:** batch 500, hourly. That is a full pass every 2.4 days, which is a reasonable
freshness target for long-form writing that is published weekly at best. Raise it only
after measuring how long a real run takes.

## Constraints to respect

**Stay well inside the timeout.** The cursor is written only after a run finishes, so a run
killed at 30 minutes records no progress and the same slice is retried next time. Work is
not lost or duplicated in the index, but it is wasted. Target a run that finishes in about
half the ceiling, and lower `BLOGME_DISCOVERY_BATCH` if runs creep up.

Each source also carries its own 90-second deadline. One source can otherwise string
together a robots fetch, several sitemap probes and a page fetch per post, each with its own
client timeout, so its worst case was the sum of all of them — which made a run's length a
hope rather than a calculation. Whatever a slow source gathered before its deadline is still
kept.

**Batch size is bounded by concurrency, not by the timeout alone.** Processed one at a
time, a few hundred sources will not fit in 30 minutes. A bounded pool of concurrent
fetches is what makes larger batches viable.

**Be polite to the sites being crawled.** Cadence is not only an internal capacity
question:

- Honour robots.txt, per [RFC 9309](https://www.rfc-editor.org/rfc/rfc9309). Done, including
  wildcards, `$` anchors, `Allow` and longest-match precedence. Matching used to be a literal
  prefix comparison, which meant every wildcard rule silently failed to match and the crawler
  fetched exactly what the site had asked it not to.
- Limit concurrency per host, not just overall. Done, and by registrable domain rather
  than by hostname — shared platforms put thousands of sources on one server, each as its
  own subdomain, so a per-hostname cap would not have limited anything.
- Send conditional requests. **Not yet implemented, and the largest saving still
  available.** Feeds normally support `ETag` and `If-Modified-Since`, so an unchanged feed
  would cost a `304` rather than a full download. Today every pass re-fetches every feed
  in its slice in full, and re-fetches the page behind any post whose feed entry is a
  stub. Doing this is what makes a faster cadence cheap rather than merely possible.

**Feeds and sitemaps cost differently.** 68% of sources publish a feed, which is one cheap
request. The remaining 9,048 need a sitemap walk, which is heavier. If cadence becomes
expensive, checking feed-backed sources more often than sitemap-only ones is the obvious
split, but it is not worth the complexity until measurements justify it.

**Storage caps bite before compute does.** The 50 MB Free-tier ceiling was reached first,
which is why the service now runs on Basic; see [tech-stack.md](tech-stack.md). Cadence
sets how fast the next ceiling arrives, so check index size against the
[service limits](https://learn.microsoft.com/en-us/azure/search/search-limits-quotas-capacity)
before raising it. On 17 August 2026 the index held 4,847 documents in 23.5 MB, or 0.15%
of Basic's 15 GB, so the ceiling is far off at the current cadence. Truncating articles to
1,000 words is what keeps a document small enough for this to stay a slow problem — that
cap is the main lever on index size, so raising it for recall and raising cadence for
freshness both spend the same budget.

## Changing it

```bash
az functionapp config appsettings set \
  --name <FUNCTION_APP> --resource-group <RESOURCE_GROUP> \
  --settings BLOGME_DISCOVERY_BATCH=500 BLOGME_DISCOVERY_SCHEDULE="0 0 * * * *"
```

Applying this restarts the app, so confirm `/api/health` returns `200` afterwards. Read
the values back with:

```bash
az functionapp config appsettings list \
  --name <FUNCTION_APP> --resource-group <RESOURCE_GROUP> \
  --query "[?starts_with(name,'BLOGME_DISCOVERY')].{name:name,value:value}" -o table
```

The schedule is a six-field NCRONTAB expression, where the first field is seconds.

| Expression       | Meaning          |
| ---------------- | ---------------- |
| `0 0 */6 * * *`  | Every six hours  |
| `0 0 * * * *`    | Hourly           |
| `0 */30 * * * *` | Every 30 minutes |

## When batching stops being enough

Batching is the simple answer, and it holds while a run's work fits comfortably in one
invocation. Move to a queue with parallel workers, as anticipated in
[system-design.md](system-design.md), when either:

- a batch large enough to keep pace no longer fits in the timeout, even with concurrency; or
- runs regularly approach the ceiling and the cursor stops advancing reliably.

At that point a planner enqueues one message per source and workers process them
independently, so throughput scales with instance count instead of with the length of a
single run.

## References

- [Azure Functions timer trigger](https://learn.microsoft.com/en-us/azure/azure-functions/functions-bindings-timer)
- [NCRONTAB expressions](https://learn.microsoft.com/en-us/azure/azure-functions/functions-bindings-timer#ncrontab-expressions)
- [Azure Functions Flex Consumption plan](https://learn.microsoft.com/en-us/azure/azure-functions/flex-consumption-plan)
- [Azure AI Search service limits](https://learn.microsoft.com/en-us/azure/search/search-limits-quotas-capacity)
- [RFC 9309 — Robots Exclusion Protocol](https://www.rfc-editor.org/rfc/rfc9309)
- [Sitemaps Protocol](https://www.sitemaps.org/protocol.html)
