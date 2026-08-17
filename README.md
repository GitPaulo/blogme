# blogme

A search engine to find tech blogs.

<img width="964" height="1732" alt="image" src="https://github.com/user-attachments/assets/aa6a5bcd-8e38-4f27-b889-092c62dd5eca" />

> **Note from me:** This is freshly slopped but serving the purpose. I needed a way to browser through curated lists of tech blogs and keep track of my reading. I will update it sparsely to suit my reading purposes.

## Dev

The dev container installs everything. Then:

```bash
make setup   # install dependencies, create local config
make dev     # Azurite + Functions host + Vite dev server
```

- Web: <http://localhost:5173>
- API: <http://localhost:7071/api/search?q=test>
