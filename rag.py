#!/usr/bin/env python3
"""rag.py — apply frag's changed_chunks.jsonl and answer questions.

Usage:
  python rag.py update              # apply changed_chunks.jsonl into .rag-store.pkl
  python rag.py ask "<question>"    # retrieve top-k and answer via OpenAI

Requires: pip install openai ; OPENAI_API_KEY set.
"""
import json, math, pickle, sys
from pathlib import Path
from openai import OpenAI

STORE = Path(".rag-store.pkl")
JSONL = Path("changed_chunks.jsonl")
EMBED_MODEL = "text-embedding-3-small"
CHAT_MODEL = "gpt-4o-mini"
TOP_K = 5

client = OpenAI()


def load_store() -> dict:
    return pickle.loads(STORE.read_bytes()) if STORE.exists() else {}


def save_store(s: dict) -> None:
    STORE.write_bytes(pickle.dumps(s))


def embed(texts: list[str]) -> list[list[float]]:
    r = client.embeddings.create(model=EMBED_MODEL, input=texts)
    return [d.embedding for d in r.data]


def cosine(a: list[float], b: list[float]) -> float:
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    return dot / (na * nb) if na and nb else 0.0


def cmd_update() -> None:
    if not JSONL.exists():
        sys.exit(f"missing {JSONL} — run `frag sync <url> && frag export` first")
    store = load_store()
    to_embed: list[tuple[str, str, str]] = []
    removed = 0
    for line in JSONL.read_text().splitlines():
        if not line.strip():
            continue
        rec = json.loads(line)
        cid, ct = rec["chunk_id"], rec["change_type"]
        if ct == "removed":
            if store.pop(cid, None) is not None:
                removed += 1
        else:
            to_embed.append((cid, rec["url"], rec["text"]))
    if to_embed:
        vecs = embed([t for _, _, t in to_embed])
        for (cid, url, text), v in zip(to_embed, vecs):
            store[cid] = {"url": url, "text": text, "embedding": v}
    save_store(store)
    print(f"updated: {len(to_embed)} embedded, {removed} removed; store now has {len(store)} chunks")


def cmd_ask(question: str) -> None:
    store = load_store()
    if not store:
        sys.exit("store empty — run `python rag.py update` first")
    qv = embed([question])[0]
    ranked = sorted(store.items(), key=lambda kv: cosine(qv, kv[1]["embedding"]), reverse=True)[:TOP_K]
    ctx = "\n\n---\n\n".join(f"[source: {v['url']}]\n{v['text']}" for _, v in ranked)
    resp = client.chat.completions.create(
        model=CHAT_MODEL,
        messages=[
            {"role": "system", "content": "Answer the user's question using ONLY the context below. If the context does not contain the answer, say so plainly. Cite the source URL when you can."},
            {"role": "user", "content": f"Context:\n{ctx}\n\nQuestion: {question}"},
        ],
    )
    print(resp.choices[0].message.content)


def main() -> None:
    if len(sys.argv) < 2 or sys.argv[1] not in ("update", "ask"):
        sys.exit('usage: python rag.py update | python rag.py ask "<question>"')
    if sys.argv[1] == "update":
        cmd_update()
    else:
        if len(sys.argv) < 3:
            sys.exit('usage: python rag.py ask "<question>"')
        cmd_ask(" ".join(sys.argv[2:]))


if __name__ == "__main__":
    main()
