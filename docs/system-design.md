# System Design

> **Goal:** simplest practical architecture that can grow without requiring an early redesign.

## Architecture

```text
GitHub Repository
      │
      ├── GitHub Pages ──────────────┐
      │   Static frontend            │
      │                              ▼
      └── Azure Functions ─────> Azure AI Search
          Search API                 Search index
          Discovery job
                │
                ▼
          Azure Storage
          Canonical article data
```

## Components

### GitHub Pages

Hosts the static search website.

- UI only.
- Calls the Azure Functions search API.
- Deployed from GitHub.

### Azure Functions — Flex Consumption

Single backend application containing:

- **Search API** — HTTP-triggered endpoint used by the frontend.
- **Discovery job** — timer-triggered job that finds and processes new articles.

Use **Flex Consumption**. It is Microsoft's recommended serverless Azure Functions hosting plan for new applications and supports automatic event-driven scaling.

### Azure Storage

Stores the canonical article data, for example cleaned article JSON.

It can also use the same general-purpose storage account required by the Function App initially, with separate containers.

The search index is treated as rebuildable; Azure Storage remains the source of truth.

### Azure AI Search

Stores the searchable representation of articles.

Start with **Dedicated Basic**:

- Full-text search initially.
- Add vector/hybrid search when needed.
- Scale replicas for query load and partitions for index capacity.
- Move to Standard only when workload requires it.

Do **not** depend on the new Serverless Developer tier initially. As of August 2026 it is still Preview, has no SLA, and Microsoft does not recommend it for production workloads.

## Data Flow

### Discovery

```text
Timer
  ↓
Azure Function
  ↓
Discover + process article
  ↓
Azure Storage
  ↓
Azure AI Search
```

### Search

```text
User
  ↓
GitHub Pages
  ↓
Azure Function
  ↓
Azure AI Search
  ↓
Results
```

## Authentication

Use a **Managed Identity** for the Function App to access Azure Storage and Azure AI Search.

No Azure credentials should be exposed to the GitHub Pages frontend.

## Source Configuration

Keep the approved blog/source list in Git initially.

```text
sources/
  blogs.yml
```

Changes go through the normal Git workflow and require no additional database or admin service.

## Scaling Path

Only add infrastructure when a real limit appears:

1. **More search traffic/data** → scale Azure AI Search.
2. **Discovery becomes heavy** → separate the discovery worker from the API.
3. **Discovery needs parallel processing** → introduce a queue and workers.
4. **GitHub Pages becomes limiting** → migrate only the frontend hosting.

No Cosmos DB, Service Bus, API Management, Kubernetes, or separate microservices initially.

## References

Validated against official documentation on **16 August 2026**.

- [Azure Functions hosting options](https://learn.microsoft.com/en-us/azure/azure-functions/functions-scale)
- [Azure Functions Flex Consumption](https://learn.microsoft.com/en-us/azure/azure-functions/flex-consumption-plan)
- [Azure Functions timer trigger](https://learn.microsoft.com/en-us/azure/azure-functions/functions-bindings-timer)
- [Azure Functions HTTP trigger](https://learn.microsoft.com/en-us/azure/azure-functions/functions-bindings-http-webhook-trigger)
- [Azure Functions storage considerations](https://learn.microsoft.com/en-us/azure/azure-functions/storage-considerations)
- [Azure Blob Storage](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blobs-introduction)
- [Azure AI Search](https://learn.microsoft.com/en-us/azure/search/search-what-is-azure-search)
- [Azure AI Search hybrid search](https://learn.microsoft.com/en-us/azure/search/hybrid-search-overview)
- [Azure AI Search tiers](https://learn.microsoft.com/en-us/azure/search/search-sku-tier)
- [Azure AI Search scaling](https://learn.microsoft.com/en-us/azure/search/search-create-service-portal)
- [Azure AI Search RBAC](https://learn.microsoft.com/en-us/azure/search/search-security-rbac)
- [GitHub Pages](https://docs.github.com/en/pages/getting-started-with-github-pages/what-is-github-pages)
