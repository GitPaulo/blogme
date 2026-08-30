# blogme

A search engine to find tech blogs.

<img width="1004" height="1162" alt="image" src="https://github.com/user-attachments/assets/cafcb5ee-a991-4040-ad45-5091a7b9bee9" />

> **Note from me:** This is freshly slopped but serving the purpose. I needed a way to browser through curated lists of tech blogs and keep track of my reading. I will update it sparsely to suit my reading purposes.

## Docs

| Document                                                         | What it covers                                      |
| ---------------------------------------------------------------- | --------------------------------------------------- |
| [How it works](docs/how-it-works.md)                             | End to end: a list of blogs becomes a search result |
| [System design](docs/system-design.md)                           | Which services run it, and why so few               |
| [Tech stack](docs/tech-stack.md)                                 | What it is written in, and how to work on it        |
| [Discovery cadence](docs/discovery-cadence.md)                   | How often the crawler runs, and what alerts on it   |
| [Quality scoring](docs/quality-scoring.md)                       | How an article is judged apart from any query       |
| [Sources](sources/README.md)                                     | The blog list, and the tool that builds it          |
| [High-level plan](docs/blog-discovery-search-high-level-plan.md) | The original brief                                  |

## Dev

The dev container installs everything. Then:

```bash
make setup   # install dependencies, create local config
make dev     # Azurite + Functions host + Vite dev server
```

- Web: <http://localhost:5173>
- API: <http://localhost:7071/api/search?q=test>

Search has no emulator, so a dev host queries the live index and needs `az login`.
`make dev` fetches a read-only query key for it; set `BLOGME_SEARCH_ENDPOINT` and
`BLOGME_SEARCH_API_KEY` yourself to point at another service.
