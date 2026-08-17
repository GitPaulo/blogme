# Blog source extractor

Builds [`sources/blogs.yml`](../blogs.yml) from the blog lists named in
[`source_lists.txt`](source_lists.txt). A seed is either a GitHub repository or topic,
whose list files are read, or the URL of a web page that links to blogs.

Every link found in those lists is checked. A link whose site answers an HTTP request
becomes a source entry; a feed is recorded when the site publishes one but is not
required.

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

| Flag | Purpose |
| --- | --- |
| `--require-feed` | Keep only sites with a working RSS/Atom feed |
| `--limit-candidates N` | Check the first N links only, for a quick trial run |
| `--concurrency N` | Concurrent site checks, default 200 |
| `--output PATH` | Write somewhere other than `sources/blogs.yml` |

The full run checks roughly 49k links and takes a couple of hours; progress and an
estimate are printed to stderr.

## Output

`sources/blogs.yml`:

```yaml
sources:
  - id: example
    name: Example
    site: https://example.com/
    feed: https://example.com/feed.xml
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
  output.py          ids, validation, YAML and CSV writing
```

## How links become sources

- Article URLs collapse to the blog they belong to, so a list of fifty posts from one
  site produces one entry. Multi-tenant hosts (`medium.com/@user`,
  `*.github.io/project/`) and `/blog/` style paths are preserved.
- OPML lists carry each blog's feed URL, so those feeds are used directly instead of
  being guessed, which is the difference between one request per blog and up to a dozen.
- On an HTML list, a link's text is kept as the blog's name, but only when the link
  points at the blog itself: a list of posts links to articles, where the text is the
  article's title. It is the weakest naming signal and only surfaces when the site
  offers nothing better, so a blog with a feed or a `<title>` is unaffected.
- A list's links back to its own domain are dropped: navigation and credits are not
  blogs. Relative links are dropped for the same reason rather than resolved.
- Sites already in `blogs.yml` keep their `id` across a rebuild.
- Redirects are followed and the final URL is recorded, so `http` and `www` variants
  merge into one entry.
- Names come from the site's feed title, `og:site_name` or `<title>`, falling back to
  the link text in the list and then the domain.
- Tags are scored against [`tags.yml`](tags.yml): a feed category is worth 2 points, each
  distinct keyword in the blog's own text 1, and a tag needs 2 to be kept. Blogs that say
  too little fall back to the coarse subject their source list implies.
- Code hosts, social networks, badges, shorteners and asset subdomains are never
  emitted as sources; the GitHub list repositories themselves are inputs only.
- The output is validated before it is written: required fields, unique ids and sites,
  well-formed URLs and tag conventions.

## Scheduled runs

[`build-sources.workflow.yml`](build-sources.workflow.yml) is a draft GitHub Actions
workflow. Copy it to `.github/workflows/` to enable it; it uploads the regenerated list
as an artifact rather than committing, so changes still go through review.
