# Blog source extractor

Builds [`sources/blogs.yml`](../blogs.yml) from the blog lists named in
[`source_lists.txt`](source_lists.txt). A seed is either a GitHub repository or topic,
whose list files are read, or the URL of a web page that links to blogs.

Every link found in those lists is checked. A link whose site answers an HTTP request
becomes a source entry; a feed is recorded when the site publishes one but is not
required.

## Adding a source list

Add one line to [`source_lists.txt`](source_lists.txt) and rebuild. There is nothing
else to register.

A seed is written as a URL, and its form decides how it is read:

| Form                     | What is read                                              |
| ------------------------ | --------------------------------------------------------- |
| `github.com/owner/repo`  | Every list file in the repository                         |
| `github.com/topics/name` | The topic's most-starred repositories, then as above      |
| `https://host/page.html` | One HTML page; its links are the blogs                    |
| `https://host/list.opml` | A subscription list; each entry gives a site and a feed   |
| `https://host/list.yaml` | Any structured file: `.yaml` `.json` `.csv` `.xml` `.tsv` |

**Prefer a file over a repository when the list publishes one.** An OPML entry names
the blog and gives its feed outright, which is the difference between one request per
blog and up to a dozen spent guessing feed paths. It also skips the rest of the
repository, so a list wrapped in a website does not drag its own navigation in.

A page URL is fetched as-is, so it can point at a file inside a repository. That is how
one category of a large list can be taken without the rest:

```
https://raw.githubusercontent.com/owner/repo/main/lists/Programming.opml
```

Two things decide whether a repository seed works. Files are read when their extension
is a list format, or when they are named `readme`, `blogs`, `feeds`, `list` and so on —
so a list kept in an unusual format is picked up but may still yield nothing. And within
a file that is not a structured format, only lines that look like entries are read:
bullets, numbered items, table rows, and lines that are just a URL. Prose that mentions
a blog in passing is skipped, which is deliberate — a recommendation is a list entry.

That is why `blogscroll/blogscroll` is seeded as its published OPML rather than as the
repository: its data lives in `.toml` files, whose `url = "..."` lines are not list
entries, so the repository yields nothing.

Check a seed before committing it:

```bash
cd sources/tools
.venv/bin/python -c "
import asyncio, httpx
from extractor.sourcelists import GitHubClient, scan_page
async def main():
    async with httpx.AsyncClient(timeout=30) as c:
        found = await scan_page(GitHubClient(c, None), 'https://blogscroll.com/index.opml')
        print(len(found), 'blogs,', sum(1 for v in found.values() if v.feed), 'with a feed')
asyncio.run(main())
"
```

A seed that returns nothing is reading the wrong file or the wrong format. For a whole
repository, use `scan_repo` instead, or just run the build with `--limit-candidates`.

Two other things follow from the seed's name. Subject fallbacks and the `kind` field are
inferred from words in the seed URL — `personal`, `company`, `smallweb`, `security`,
`frontend` and so on — so a list whose name does not describe what it collects should be
added to `provenance_tags_for_seed` in [`extractor/tags.py`](extractor/tags.py).

## Run

```bash
cd sources/tools
python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
GITHUB_TOKEN=$(gh auth token) .venv/bin/python build_sources.py
```

On Windows a venv puts its executables in `.venv/Scripts/` instead, and `python3` is a
Microsoft Store stub that cannot create one — use `python` and `.venv/Scripts/`. `make`
handles this difference itself, where `make` is installed.

A token is strongly recommended. Without one, repository files are read from
`raw.githubusercontent.com`, which rate-limits by IP and starts refusing requests part
way through a full run; with one they are read through the GitHub API, where the
allowance is far higher. The token is only ever sent to GitHub, never to a list page.
From the repository root, `make sources` does the same thing.

Useful flags:

| Flag                    | Purpose                                                |
| ----------------------- | ------------------------------------------------------ |
| `--require-feed`        | Keep only sites with a working RSS/Atom feed           |
| `--limit-candidates N`  | Check the first N links only, for a quick trial run    |
| `--concurrency N`       | Concurrent site checks, default 200                    |
| `--retry-concurrency N` | Concurrent checks in the retry pass, default 50        |
| `--output PATH`         | Write somewhere other than `sources/blogs.yml`         |
| `--overrides PATH`      | Corrections to merge in, default `blogs-overrides.yml` |

The full run checks roughly 49k links and takes a couple of hours; the retry pass over the
sites that did not answer adds to that, since it deliberately runs at a quarter of the
concurrency. Progress and an estimate are printed to stderr.

## Output

`sources/blogs.yml`:

```yaml
sources:
  - id: example
    name: Example
    site: https://example.com/
    feed: https://example.com/feed.xml
    kind: [personal-blogs]
    tags: [software-engineering, tech]
```

`link-audit.csv` (not committed) lists every discovered link with `reachable`, the HTTP
status or failure reason, the feed, the tags and the repository file it came from. It is
the place to look when a blog you expected is missing.

## Layout

```
build_sources.py     CLI and orchestration
tags.yml             the tag vocabulary
extractor/
  progress.py        stderr logging and progress counter
  models.py          the Candidate record passed between stages
  urls.py            link cleaning, collapsing a link to its blog
  tags.py            tag scoring, driven by tags.yml
  naming.py          title, description and feed links from an HTML head
  sourcelists.py     GitHub API access and link extraction
  checks.py          reachability and feed discovery
  overrides.py       merging blogs-overrides.yml into the generated list
  output.py          ids, validation, YAML and CSV writing
```

## How links become sources

- Article URLs collapse to the blog they belong to, so a list of fifty posts from one
  site produces one entry. Multi-tenant hosts (`medium.com/@user`,
  `*.github.io/project/`) and `/blog/` style paths are preserved.
- A site that answers is in and a site that refuses is out, but a site that says nothing
  gets a second, slower attempt. At 200 concurrent checks a connect timeout is as likely
  to describe the run as the site: sampling the links dropped that way found roughly
  three quarters of them answering on a longer connect budget. Anything that returned an
  HTTP status, including a 404 or a 403, is not retried — that was an answer.
- OPML lists carry each blog's feed URL, so those feeds are used directly instead of
  being guessed, which is the difference between one request per blog and up to a dozen.
- On an HTML list, a link's text is kept as the blog's name, but only when the link
  points at the blog itself: a list of posts links to articles, where the text is the
  article's title. It is the weakest naming signal and only surfaces when the site
  offers nothing better, so a blog with a feed or a `<title>` is unaffected.
- A list's links back to its own domain are dropped: navigation and credits are not
  blogs. Relative links are dropped for the same reason rather than resolved.
- Sites already in `blogs.yml` keep their `id` across a rebuild, and their `feed`
  unless this run proved it gone. A feed lookup that timed out is not proof: treating
  it as such once dropped a feed the blog was still publishing, which moved it to the
  sitemap path and — having no sitemap — out of the crawl entirely.
- [`blogs-overrides.yml`](../README.md#corrections-by-hand) is merged in last, so a
  correction made by hand outlives the rebuild. Its entries are validated like any
  other, and an unknown field fails the build rather than being ignored.
- Redirects are followed and the final URL is recorded, so `http` and `www` variants
  merge into one entry.
- Names come from the site's feed title, `og:site_name` or `<title>`, falling back to
  the link text in the list and then the domain.
- Tags are scored against [`tags.yml`](tags.yml): a feed category is worth 2 points, each
  distinct keyword in the blog's own text 1, and a tag needs 2 to be kept. Blogs that say
  too little fall back to the coarse subject their source list implies.
- What sort of blog it is (`personal-blogs`, `company-blogs`) is written to `kind` rather
  than to `tags`: it comes from the list a blog was found in, not from the blog, and it
  applies to so many of them that as a subject tag it would say nothing.
- Code hosts, social networks, badges, shorteners and asset subdomains are never
  emitted as sources; the GitHub list repositories themselves are inputs only.
- The output is validated before it is written: required fields, unique ids and sites,
  well-formed URLs and tag conventions.

## Scheduled runs

[`build-sources.workflow.yml`](build-sources.workflow.yml) is a draft GitHub Actions
workflow. Copy it to `.github/workflows/` to enable it; it uploads the regenerated list
as an artifact rather than committing, so changes still go through review.
