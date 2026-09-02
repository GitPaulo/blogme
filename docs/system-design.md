# System Design

> **Goal:** the simplest architecture that can grow without an early redesign.
> Companion to [tech-stack.md](tech-stack.md), which decides what it is written in.

## Architecture

```text
GitHub Repository
      │
      ├── GitHub Pages ──────────────┐
      │   Static frontend            │
      │                              ▼
      └── Azure Functions ─────> Azure AI Search
          HTTP: search, suggest,     Search index
                health              ▲
          Timers: discover, score   │
                │                   │
                ▼                   │
          Azure Storage ────────────┘
          Canonical article data
```

## Components

### GitHub Pages

Hosts the static search website. UI only: it calls the Functions HTTP API and holds no
Azure credentials.

### Azure Functions — Flex Consumption

One backend application, five functions:

| Function   | Trigger | What it does                                       |
| ---------- | ------- | -------------------------------------------------- |
| `search`   | HTTP    | Answers a query from the index                     |
| `suggest`  | HTTP    | Completes the query being typed                    |
| `health`   | HTTP    | Asks the index for a document count                |
| `discover` | Timer   | Crawls a slice of the source list for new articles |
| `score`    | Timer   | Judges indexed articles for quality                |

Flex Consumption is Microsoft's recommended serverless plan for new applications, and
the only one first-class Go support runs on. Timers registered separately so either can
be turned off alone.

### Azure Storage

Holds the canonical article JSON, plus the small amount of state the timers carry between
runs — the discovery cursor, per-source crawl health, and site popularity — in containers
on the same general-purpose account the Function App already requires. It is the source of
truth. Only the articles are irreplaceable; the rest is rebuilt by observation if lost,
which is why a pass warns and continues rather than failing when it cannot read them.

### Azure AI Search

Holds the searchable projection of those articles. The index is treated as
**rebuildable** from storage, which is what makes changing tier or schema cheap. Tier
choice and its history are in [tech-stack.md](tech-stack.md#data-and-search).

## Data flow

```text
Discovery                      Search
  Timer                          User
    ↓                              ↓
  Azure Function                 GitHub Pages
    ↓                              ↓
  Discover + process article     Azure Function
    ↓                              ↓
  Azure Storage                  Azure AI Search
    ↓                              ↓
  Azure AI Search                Results
```

Step by step, see [how-it-works.md](how-it-works.md).

## Authentication

The Function App uses a **managed identity** to reach Azure Storage and Azure AI Search.
No Azure credential reaches the GitHub Pages frontend, and the deploy workflows
authenticate with OIDC federation rather than a stored secret.

## Source configuration

The approved blog list lives in Git, so every change goes through normal review and no
database or admin service is needed:

```text
sources/
  blogs.yml            generated, the approved list
  blogs-overrides.yml  corrections kept by hand, re-applied on every rebuild
```

Publishing the list is separate from deploying the code — see
[sources/README.md](../sources/README.md#publishing).

## Scaling path

Add infrastructure only when a real limit appears:

1. **More search traffic or data** → scale Azure AI Search.
2. **Discovery becomes heavy** → separate the discovery worker from the API.
3. **Discovery needs parallel processing** → introduce a queue and workers.
4. **GitHub Pages becomes limiting** → migrate only the frontend hosting.

The trigger for step 3 is spelled out in
[discovery-cadence.md](discovery-cadence.md#when-batching-stops-being-enough).

No Cosmos DB, Service Bus, API Management, Kubernetes or separate microservices.

## References

Validated against official documentation on **16 August 2026**.

- [Azure Functions hosting options](https://learn.microsoft.com/en-us/azure/azure-functions/functions-scale)
- [Azure Functions Flex Consumption](https://learn.microsoft.com/en-us/azure/azure-functions/flex-consumption-plan)
- [Azure Functions timer trigger](https://learn.microsoft.com/en-us/azure/azure-functions/functions-bindings-timer)
- [Azure Functions HTTP trigger](https://learn.microsoft.com/en-us/azure/azure-functions/functions-bindings-http-webhook-trigger)
- [Azure Functions storage considerations](https://learn.microsoft.com/en-us/azure/azure-functions/storage-considerations)
- [Azure Blob Storage](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blobs-introduction)
- [Azure AI Search](https://learn.microsoft.com/en-us/azure/search/search-what-is-azure-search)
- [Azure AI Search tiers](https://learn.microsoft.com/en-us/azure/search/search-sku-tier)
- [Azure AI Search RBAC](https://learn.microsoft.com/en-us/azure/search/search-security-rbac)
- [GitHub Pages](https://docs.github.com/en/pages/getting-started-with-github-pages/what-is-github-pages)
