# blogme

A search engine to find tech blogs.

<img width="1004" height="1162" alt="image" src="https://github.com/user-attachments/assets/cafcb5ee-a991-4040-ad45-5091a7b9bee9" />

> **Note from me:** This is freshly slopped but serving the purpose. I needed a way to browser through curated lists of tech blogs and keep track of my reading. I will update it sparsely to suit my reading purposes.

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
