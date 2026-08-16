# Blog Discovery Search — High-Level Plan

## 1. Goal

Build a search engine for discovering **high-quality long-form blog posts** about software engineering, astrophysics, science, and other technical subjects.

The product should make it easy to:

- Search for posts using **keywords or natural-language descriptions**.
- Discover useful writing from authors and sites the user may not already know.
- Focus on **substantive, educational, independent writing**, rather than the wider web.
- Return results based on both **relevance** and **content quality**.
- Grow into a curated knowledge/discovery system rather than trying to compete with a general-purpose web search engine.

A typical use should feel like:

> “I want to read something good about scaling single-threaded servers.”

or:

> “Find articles explaining how satellites can be mistaken for meteors.”

The core value is **finding worthwhile things to read**, not answering the question directly.

---

## 2. Discovery

Discovery determines **what content is allowed into the searchable corpus**.

Start from trusted sources such as:

- Curated blog lists.
- Blogs already known to contain good material.
- User or community submissions.
- RSS/Atom feeds and sitemaps from approved sites.
- Links from existing high-quality articles to other potentially useful blogs.

Discovery should be **selective rather than exhaustive**. The aim is not to crawl the entire web, but to steadily expand a corpus of worthwhile technical and scientific writing.

At a high level:

```text
Known good sources
        ↓
Discover their articles
        ↓
Find additional promising sources
        ↓
Review / accept sources
        ↓
Add their articles to the searchable corpus
```

The system should revisit accepted sources periodically so new posts become searchable automatically.

Any automated discovery or crawling should respect website owners' published crawler rules.

---

## 3. Search

Search should support both **exact topics** and **ideas expressed in normal language**.

Examples:

```text
text AI watermarking
```

```text
problems scaling single-threaded servers
```

```text
how can I tell a satellite from a meteor in a photograph
```

At a high level, search should combine:

- **Keyword relevance** — matching important words and phrases.
- **Semantic relevance** — finding articles about the same idea even when different wording is used.
- **Quality signals** — preferring useful, substantive articles over weak matches.

Results should remain simple:

```text
Article title
Author / publication
Short description
Relevant topics
Publication date
```

The ranking goal is:

> **Show the most relevant high-quality articles that are worth reading.**

Over time, search can evolve into broader discovery features such as:

- Related articles.
- “More like this.”
- Topic exploration.
- Personalised recommendations based on saved or previously enjoyed articles.

These are extensions of the search corpus rather than requirements for the initial product.

---

## References

- Sean Goedecke, *Text AI watermarks will always be trivial to remove*  
  https://www.seangoedecke.com/text-ai-watermarks/
- Alyn Wallace, *Sorry, That's No Meteor, It's A Satellite*  
  https://alynwallacephotography.com/blog/2020/4/21/sorry-thats-no-meteor-its-a-satellite
- `letsila/awesome-blogs` — example curated blog source list  
  https://github.com/letsila/awesome-blogs
- RFC 9309 — Robots Exclusion Protocol  
  https://www.rfc-editor.org/rfc/rfc9309
- Sitemaps Protocol  
  https://www.sitemaps.org/protocol.html
