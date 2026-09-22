# memora — Recall a position you cannot name

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

## Recall a position you cannot name

`SHOW ROUTES` walks down from a node you already chose. When you cannot name that
node, recall answers the other question: **where in the semantic tree does this
topic live?** It is a locator, not an answer — it returns no fact, no score, no
distance, no rank, no reason, and not the text it matched.

```sh
memora query --input '{"parameters":{"named":{"q":"存储引擎","limit":5}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "RECALL FROM work MATCH :q LIMIT :limit"
memora query --input '{"parameters":{"named":{"q":"存储引擎","limit":5}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "RECALL FROM work IN notes MATCH :q LIMIT :limit"
```

Each hit carries `database`, `table`, `kind`, an optional `object_id`, and `path`
— the root-first segments, each with the `route_id` navigation needs. Continue
exactly as after discovery: `OPEN ROUTE` the last segment, then `SELECT` the fact.
Recall never prefetches: it does not open the leaf, cache the row, or substitute
for the `SELECT` that produces the answer.

A recall may come back with a `vectors_not_ready` warning. It is not an error:
it says how many units in the scope no vector path can answer for yet, so the
paths you got are real but the list may be incomplete. Read `not_ready_units`
(and `identity_locked`, which separates “nobody configured embeddings” from
“some units went stale”) and say so instead of presenting the result as
exhaustive. Do not retry hoping for more — `RECALL` never waits for embeddings;
`memora doctor` reports the same count for the whole instance.

If you already hold the vector for exactly what you are about to write, you can
attach it to the write itself (`mutation.vector`, alongside `route_path`) and it
lands in the same transaction. The `content_hash` must be the hash of the text
you embedded: the engine recomputes it from what the Row actually holds, refuses
a mismatch, and writes the Row anyway — a warning on the result says the unit is
still not-ready, which is the difference between a lost vector and a silent one.

If this host has an embedding provider configured, `memora exec` already does the
draining for you: after a write commits it asks what units are missing vectors,
embeds them, and offers the vectors back — a failure there never fails the write,
and it says so on stderr. What follows is the manual path for when you compute
embeddings yourself.

If you compute embeddings yourself, drain the backlog in three steps: ask what is
missing, embed the text each unit hands you, then offer each vector back.

```sh
memora query --input '{"parameters":{"named":{"limit":32}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW PENDING VECTORS IN DATABASE work LIMIT :limit"
```

`SHOW PENDING VECTORS` returns `unit_no`, `table`, `content_hash` and `payload` —
the work list, not an answer: it is what makes a backlog drainable even when the
Rows were written by someone else. Embed `payload`, then offer the vector back:

If you compute embeddings yourself, you can offer one to a unit — that is how a
backlog gets drained, one statement per unit, as many statements as you like in
one request:

```sh
memora exec --input '{"parameters":{"named":{"v":"<base64>","unit":42,"model":"text-embedding-v4","hash":"sha256:..."}},"mutation":{"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-9","reason":"attach embedding"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "ACCEPT VECTOR :v FOR UNIT :unit IN DATABASE work MODEL :model HASH :hash"
```

A batch is several statements in one request, and `--input` takes **one object per
statement as an array** — one `ACCEPT VECTOR`, one input, in source order:

```sh
memora exec --input '[{"parameters":{"named":{"v":"<b64>","unit":42,"model":"text-embedding-v4","hash":"sha256:a"}},"mutation":{"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-9","reason":"attach embeddings"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}},{"parameters":{"named":{"v":"<b64>","unit":43,"model":"text-embedding-v4","hash":"sha256:b"}},"mutation":{"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-9","reason":"attach embeddings"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}]' "ACCEPT VECTOR :v FOR UNIT :unit IN DATABASE work MODEL :model HASH :hash; ACCEPT VECTOR :v FOR UNIT :unit IN DATABASE work MODEL :model HASH :hash"
```

Your provider may cap how many texts one embedding request may carry; that is the
host's business, not the language's (`MEMORA_EMBEDDING_BATCH` declares the cap, and
the CLI splits a refused batch on its own).

`HASH` is the hash of the text you embedded, not of the Row: the engine recomputes
it and refuses a mismatch, so an embedding of a previous revision cannot land on
the current one. A unit the engine has no vector for simply stays not-ready —
`RECALL` reports it, and nothing pretends it was attached.

When you already have a query embedding — for instance the one you just computed
for the text the user asked about — you can search by position instead of words:
`RECALL FROM <db> [IN <table>] NEAREST :v LIMIT :n`. The vector travels as
**base64 (raw URL-safe) of little-endian float32, no padding**, and must match the
Database's locked width; a vector of the wrong width, or one carrying NaN, is
refused rather than rounded. The answer has exactly the same shape as a keyword
recall — so navigate the same way — and `LIMIT` means the same thing in both:
it truncates the listing, it is not a recall strength. Asking for both arms at
once (`MATCH :q NEAREST :v`) fuses them by **rank** (reciprocal rank fusion,
`k = 60`): each arm brings its own order — the vector arm by distance, the
keyword arm by BM25 — a position both arms found outranks one only a single arm
found, ties fall back to table then path, and `LIMIT` still truncates the fused
listing. The fused order is the useful part; no score, distance or rank is ever
returned, so do not look for one and do not treat the order as a confidence
measure. If either arm cannot answer, the statement fails rather than quietly
returning the half it could.

Both derived layers can be rebuilt from the Rows, and neither is rebuilt for you.
`REPAIR RECALL UNITS` gives every live Row the unit that keyword recall needs:
Rows written before that layer existed have none, and recall cannot find what has
no unit — it says nothing about that, so `doctor`'s `broken_recall_units` is where
you notice. It drops orphaned units (whose Row is gone) too, and never modifies a
Row.

```sh
memora exec --input '{"parameters":{"named":{"limit":64}},"mutation":{"max_affected_rows":64,"actor":"agent:host","source":"conversation:event-9","reason":"rebuild the recall layer"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "REPAIR RECALL UNITS IN DATABASE work LIMIT :limit"
```

Both passes are bounded and repeatable: run them until `remaining` is zero.

The vector index is derived from the units, and you can reconcile it without
guessing: a bounded pass repairs index rows so they hold exactly what the units
hold. It never recomputes a vector — a unit whose text moved on is stale, not
broken — so repeating it until `remaining` is zero is safe.

```sh
memora exec --input '{"parameters":{"named":{"limit":64}},"mutation":{"max_affected_rows":64,"actor":"agent:host","source":"conversation:event-9","reason":"reconcile the vector index"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "REPAIR VECTOR INDEX IN DATABASE work LIMIT :limit"
```

`REPAIR VECTOR INDEX` requires a `LIMIT`, bounded to 1–1000, and it is not a
read.

`RECALL` requires a `LIMIT` bounded to 1–1000, and a query of at least **2
characters**: one character is in almost every Row, so it is refused rather than
answered with the whole Database, and a short query never comes back as an empty
list that reads like "not found".

**Recall is a text locator, never a completeness proof.** A Table named after a
topic does not follow from a query containing that topic: `RECALL … MATCH 项目`
misses a project whose title and Route names never use the word 项目. Recall
finds positions you can navigate from; the census (or a full tree walk) is what
proves nothing was missed, and the skill's `warnings` field is where an
incomplete derived layer says so.
Hits are de-duplicated by path and ordered by table then path, so the same query
over an unchanged database returns the same list. Scope is one Database, with an
optional `IN <table>`. Both arms are wired — keyword and vector — and a position
that exists but returns no hit is "not matched", never "the tree has nothing
there"; always report the query you used.

Move a Database to another embedding model

The first accepted vector pins a Database to one `(model, dimensions)` pair, and
a different model is refused from then on — not because it is worse, but because
vectors from two models are not comparable and recall returns no scores that
could show the mixture. Moving the Database is one bounded, repeatable L2
statement:

```sh
memora exec --input '{"parameters":{"named":{"limit":8,"model":"text-embedding-v4","n":1024}},"mutation":{"max_affected_rows":8,"actor":"agent:host","source":"conversation:event-9","reason":"move the Database to the configured model"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L2"}}' "REKEY VECTOR IDENTITY IN DATABASE work LIMIT :limit MODEL :model DIMENSIONS :n"
```

Repeat **the same statement, target included** until the receipt says
`rekeying` is false: the first pass opens the window and drops the derived
index, each pass releases up to `LIMIT` units, and the pass that releases the
last one locks the target. Leave `MODEL`/`DIMENSIONS` out and the Database comes
out unlocked instead, free for whatever is configured next.

A window is a refusal, not an outage: inside it `RECALL … NEAREST`,
`ACCEPT VECTOR`, `REPAIR VECTOR INDEX` and `SHOW PENDING VECTORS` all fail with
`rekey_in_progress`, keyword recall still answers, and the `vectors_not_ready`
notice names the target and how many units are left; `doctor` reports
`rekeying_databases`. **Dropping the target is also the escape hatch**: if a
rekey was started and never finished, re-issuing the statement with no
`MODEL`/`DIMENSIONS` re-aims the open window at unlocked, so a Database cannot
be left stuck. Once the window closes, the ordinary drain refills the units with
the new identity.
