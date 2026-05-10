# frag

**Chunk-level diff for RAG corpora.** Crawl a URL, hash each chunk, re-embed only what actually changed.

[![asciicast](https://asciinema.org/a/1031316.svg)](https://asciinema.org/a/1031316)

## Why

RAG systems built on web content go stale silently. The naive fix — re-embedding the entire corpus on every sync — is wasteful: if 5% of a page changed, re-embedding the other 95% is throwing money at OpenAI.

`frag` solves this at the chunk level. It crawls a URL via [Anakin](https://anakin.io)'s scraping API, splits each page's markdown into ~500-word chunks, hashes the (normalized) chunks, and diffs the new hashes against the last sync. The output is a JSONL of *exactly* the chunks that need re-embedding.

```
crawl  →  chunk  →  hash (normalized)  →  diff vs. last sync  →  embed deltas only
```

## Quick start

```bash
# build (Go 1.21+, zero external deps)
go build -o frag .

# set keys (Anakin for crawling, OpenAI for embeddings + answers)
export ANAKIN_API_KEY=...
export OPENAI_API_KEY=...

# baseline crawl + embed
./frag sync https://stripe.com/docs/upgrades
./frag export                                  # writes changed_chunks.jsonl
python3 -m venv .venv && .venv/bin/pip install openai
.venv/bin/python rag.py update                 # embeds all baseline chunks

# ask the bot
.venv/bin/python rag.py ask "What is a backward-compatible API change?"

# next day — re-sync; only changed chunks get re-embedded
./frag sync https://stripe.com/docs/upgrades   # box shows "Re-embed avoided: X%"
./frag export
.venv/bin/python rag.py update                 # only re-embeds the deltas
```

## What you see on a re-sync

```
┌─ frag sync ──────────────────────────────────────┐
│ Source:  https://stripe.com/docs/upgrades        │
│ Pages:   20/20  (job 1b8d2e32, 1m10s)            │
│                                                  │
│   ● added       1                                │
│   ● modified   10                                │
│   ● removed     0                                │
│   ○ unchanged  22                                │
│                                                  │
│ Re-embed avoided: 66.7%  (22 of 33 chunks)       │
└──────────────────────────────────────────────────┘
```

The headline is **Re-embed avoided** — that's the percentage of OpenAI embedding cost you didn't pay this sync.

## How it works

| File | Purpose |
|------|---------|
| `main.go` | CLI dispatch (`sync`, `export`), `--no-normalize` flag, summary box renderer |
| `anakin.go` | Anakin REST client — submit `POST /v1/crawl`, poll `GET /v1/crawl/{id}` until `completed`. Pure stdlib `net/http`. |
| `core.go` | Markdown chunking, sha256 hashing, cosmetic-noise normalization, diff algorithm, atomic state I/O |
| `rag.py` | Reads `changed_chunks.jsonl`, calls OpenAI embeddings on the deltas, persists store via pickle, answers questions via top-k cosine + `gpt-4o-mini` |

State lives in three local files:

- `.frag-state.json` — full snapshot of all chunks (id, url, text, hash) from the last sync
- `.frag-changes.json` — diff produced by the last sync (added / modified / removed / unchanged-count)
- `changed_chunks.jsonl` — flat per-chunk export consumed by `rag.py update` (or any embedding pipeline)

### Cosmetic-noise normalization

Pages drift between fetches even when nothing meaningful has changed — timestamps, copyright lines, dynamic IDs, HTML build comments, slightly reordered nav blocks. `frag` normalizes chunk text before hashing (not before storing), stripping things like:

- `<!-- ... -->` HTML comments
- `Last updated: ...` / `Updated: 2026-...` lines
- `© 2026 ...` / `Copyright 2026 ...`
- `Generated on ...` / `Generated at ...`
- Bare ISO timestamps (`2026-05-10T08:00`)

The original chunk text is still what gets exported and embedded — only the hash sees the normalized form. Pass `--no-normalize` to bypass for debugging.

## Sample output

`changed_chunks.jsonl` is one JSON object per changed chunk:

```jsonl
{"chunk_id":"3fcef3447150","change_type":"added","url":"https://...","text":"...","prev_text":""}
{"chunk_id":"e64717e5a630","change_type":"modified","url":"https://...","text":"...","prev_text":"..."}
{"chunk_id":"ab96872a1965","change_type":"removed","url":"https://...","text":"","prev_text":"..."}
```

Drop this into any embedder; nothing in the schema is OpenAI-specific.

## Project files

```
.
├── main.go              # 276 LOC
├── anakin.go            # 149 LOC
├── core.go              # 222 LOC
├── go.mod               # zero external deps
├── rag.py               #  96 LOC, openai SDK only
├── result.md            # full test results
├── demo.cast            # asciinema recording
└── README.md
```

Total: 743 LOC of Go + Python, zero Go deps, one Python dep (`openai`).

## Built with

- [Anakin](https://anakin.io) — web scraping & crawl API (`POST /v1/crawl`)
- [OpenAI](https://platform.openai.com) — `text-embedding-3-small` + `gpt-4o-mini`
- Go 1.21+ stdlib

## License

MIT.
