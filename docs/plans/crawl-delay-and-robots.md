# Honouring Crawl-delay, and robots.txt Properly

> A plan for the politeness layer between the crawler and the sites it reads. Companion
> to [discovery-cadence.md](../discovery-cadence.md), which owns how often a pass runs;
> this owns how hard one pass leans on a single operator.

## Where this comes from

[Issue #1](https://github.com/GitPaulo/blogme/issues/1) reported 1,887 requests to one
site in 21 seconds, recurring every couple of days. Telemetry confirmed it, at roughly a
hundred times that scale across the corpus: 1.29 million page fetches over a fortnight
for 13,521 articles, about 95 requests per article kept.

`70a955e` bounded what one source may **spend** — a per-source fetch budget, a
consecutive-failure breaker, and a short-circuit when a server answers 401, 403 or 429.
That caps the total. It does nothing about the **rate**, and the specific thing the
reporter asked for — that we honour their `Crawl-delay` — is still unimplemented.

This plan finishes the job.

## What already exists

[`robots.go`](../../api/internal/discovery/robots.go) is a real RFC 9309 implementation,
not a stub, and the design below is built on it rather than around it:

- Groups are parsed properly, including consecutive `User-agent` lines sharing one block.
- `rulesForAgent` gives a group naming us precedence over the wildcard, and treats an
  empty group addressed to us as a decision rather than an absence of one.
- `allowed` implements longest-match precedence with `Allow` winning ties, so a site that
  closes a tree and reopens one branch keeps the branch.
- `matchPath` handles `*` and a trailing `$`.
- `Sitemap` lines are collected and fed to `sitemapCandidates`.
- Rules are cached per `scheme://host` behind a mutex, so a pass fetches each robots.txt
  once.

Four things are missing or wrong.

## Gap 1 — `Crawl-delay` is not parsed

The `switch` in `parseRobots` handles `user-agent`, `allow`, `disallow` and `sitemap`.
There is no `crawl-delay` case, and no field to put one in.

It is not in RFC 9309 — it was in the 1996 draft, and Google dropped support in 2019 —
but it is still what site operators write when they want to say "slower", it is what the
reporter of issue #1 expected us to read, and a crawler that ignores it is choosing to.

## Gap 2 — an unreachable robots.txt means "crawl everything"

This is a bug, not a missing feature, and it is the most important item here.

`rulesFor` caches an empty `robotRules{}` on **any** error, and `allowed` returns `true`
when `rulesFor` fails. Empty rules allow everything, and the empty result is cached, so
the permission sticks for the rest of the pass.

RFC 9309 §2.3.1 draws a distinction we do not:

| robots.txt response      | RFC says                                        | We do    |
| ------------------------ | ----------------------------------------------- | -------- |
| 2xx                      | Parse it                                        | Parse it |
| 4xx (no file)            | Allow all                                       | Allow all |
| 5xx, timeout, DNS, reset | May assume **complete disallow**; prefer a cached copy | Allow all |

A 503 is precisely what an overloaded server returns *while it is being hammered*. Today
that reads as full permission for every subsequent page on that host — the crawler's
response to a site buckling is to stop asking whether it may proceed. The `sourceTimeout`
path has the same shape: a robots.txt that times out because the host is slow is the
strongest available signal that we should back off, and we read it as a green light.

## Gap 3 — robots.txt is truncated at 64 KB

`maxRobotsBytes = 64 << 10`. RFC 9309 §2.5 requires parsing at least 500 KiB. Large sites
routinely exceed 64 KB, and truncation lands mid-line: the tail rules vanish, and the
partial final line can parse as a shorter pattern than was written.

## Gap 4 — `Retry-After` is discarded

`refusesCrawler` now recognises 429 and stops that source, which is the important half.
The header saying *how long* is dropped, and nothing warns the other sources being
crawled concurrently on the same platform.

## The two things that make this non-trivial

**The recursion.** If a fetch waits for a host's crawl-delay, and the delay comes from
robots.txt, and robots.txt is fetched through the same fetcher, then the first request to
a host waits for a value that is fetched by a request that waits for that value.

**The key mismatch.** `Crawl-delay` is published per host, because robots.txt is per host.
The existing concurrency cap keys on `limiterKey(host)` — the registrable domain, or the
private suffix for platforms like `bearblog.dev` and `github.io`, deliberately, so a
thousand blogs on one server share two slots. Pacing must key on the **host**; the
concurrency cap stays on the **platform**. They are different limits answering different
questions, and both should apply.

## The shape

**Pace inside `acquire`, not at the call sites.** Every fetch already funnels through
`fetcher.fetch` → `acquire`. Adding a `pause` there means no future call site can forget
it. Pacing at the crawl layer instead — next to the existing `robots.allowed` calls —
would work today only because those calls happen to be near-complete, and would rot.

**Break the recursion with an explicit unpaced path**, not a flag or a context value:

```go
// getUnpaced fetches without consulting the pacer. Only robots.txt may use it: the
// pacer's answer comes from robots.txt, so fetching it through the pacer would wait on
// itself.
func (f *fetcher) getUnpaced(ctx context.Context, rawURL string, limit int64) ([]byte, error)
```

`robots.fetch` is the sole caller. One documented exception beats a boolean parameter
threaded through five signatures.

**Invert the dependency so the fetcher does not import the robots logic.** `robots`
already holds a `*fetcher`; the fetcher takes a narrow interface, wired once in `New`:

```go
// hostPacer decides how long to wait before the next request to a host.
type hostPacer interface {
    pause(ctx context.Context, host string) error
}
```

`robots` implements `pause`. A nil pacer means no pacing, which keeps every existing
`newFetcher` call in the tests working unchanged.

**A delay implies serialisation.** Two concurrent requests to a host with a 5-second
delay would each wait and then fire together, which honours the letter and not the point.
`pause` holds a per-host mutex and a `lastRequest` timestamp, so when a delay is set the
host is effectively single-threaded.

**Wait on the context, never on the clock alone**, so a source that would blow its
deadline stops promptly instead of sleeping past it:

```go
select {
case <-timer.C:
    return nil
case <-ctx.Done():
    return ctx.Err()
}
```

**Cap what we honour.** Sites publish `Crawl-delay: 86400`. Honouring that literally
means one page, then a 90-second deadline; the source is uncrawlable in a single pass and
pretending otherwise wastes a worker slot. Clamp to `maxCrawlDelay = 30s`, and where the
published delay exceeds what a pass can use, skip the source and log it with the value,
rather than half-obeying it.

## Phases

Each is independently shippable and independently testable.

1. **Parse `crawl-delay`** — `robots.go` only, no behaviour change. Add `delay` to
   `robotGroup` and `robotRules`; add the `switch` case, setting `inRules = true` like the
   other rule lines; parse with `strconv.ParseFloat` because `0.5` is common; ignore
   negatives and unparseable values. Change `rulesForAgent` to return the selected
   `robotGroup` rather than `[]robotRule`, so the delay inherits the named-beats-wildcard
   precedence already written.

2. **Pace** — the `hostPacer` interface, `pause`, `getUnpaced`, the wiring in `New`. This
   is the behaviour change and the one to watch in telemetry.

3. **Fail closed on an unreachable robots.txt** — distinguish `statusError` 4xx (allow) from
   5xx and transport errors (disallow), which `statusError` from `70a955e` already makes
   possible. Cache the negative result with a short TTL rather than for the pass, so one
   bad minute does not exclude a site for the whole run.

4. **`Retry-After`** — `fetch` already returns headers. On 429, record `blockedUntil` on the
   host in the pacer so concurrent sources on the same platform see it too.

5. **Persist across passes** *(later, needs a schema)* — a blob beside the cursor holding
   host → `{blockedUntil, consecutiveFailures}`, so a 403 or an unusable delay survives
   between hourly runs instead of being rediscovered every time. This is the difference
   between being polite within a pass and being polite over a week.

## Why this does not cost throughput

The constraint on the earlier work was that politeness must not slow the corpus down. It
does not, and the reason is structural:

- A pass is bounded by `sourceTimeout` (90s) and `crawlConcurrency` (16), not by any one
  source. A paced source occupies one of sixteen slots for at most 90 seconds — which is
  already the existing worst case for any slow source.
- Sites publishing a `Crawl-delay` are a small minority of 46,084 sources.
- Pass duration is unchanged: the deadline already truncates slow sources, and phase 1's
  budget already truncates greedy ones.

What changes is *which* pages a paced source yields per pass, not how long the pass takes.
Across the ~46-hour lap the corpus already runs on, a site with a 10-second delay still
gets crawled — just eight pages at a time instead of six hundred.

## How the bounds compose

After this there are four independent limits, each answering a different question:

| Bound              | Question                          | Set by                              |
| ------------------ | --------------------------------- | ----------------------------------- |
| `maxPerDomain`     | How many at once, per platform?    | Us                                  |
| `budget`           | How many in total, per source?     | Us (`maxPosts × 8`)                 |
| `sourceTimeout`    | For how long?                      | Us (90s)                            |
| `Crawl-delay`      | How far apart?                     | **The site**                        |

Only the last is the operator's to set, which is the point of it.

## Testing

Following the conventions already in `crawl_test.go` and `sitemap_test.go` —
`httptest.Server`, `atomic` counters, `newLocalDiscoverer` to bypass the SSRF dialer guard
on loopback:

- Record request timestamps on the stub server; assert consecutive gaps are at least the
  published delay.
- Assert concurrent requests to one paced host serialise.
- Assert `pause` returns promptly on a cancelled context rather than sleeping out the delay.
- Table tests for parsing: float values, negatives, a named group's delay beating the
  wildcard's, clamping above `maxCrawlDelay`.
- A robots.txt served as 503, asserting the crawler now fetches **nothing** from that host —
  the regression test for gap 2.
- Assert robots.txt itself is not paced, which is the recursion guard.

## Deliberately not in scope

- `Request-rate` and `Visit-time`, which are rarer than `Crawl-delay` and less honoured.
- `Host:`, which is about canonicalisation, not load.
- Any attempt to infer a polite rate from response times. Guessing is how this project
  ended up in issue #1; if the operator has not said, the existing bounds apply.
