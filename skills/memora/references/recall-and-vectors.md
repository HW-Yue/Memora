# memora — Recall a position you cannot name

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

## Recall a position you cannot name

`SHOW ROUTES` walks down from a node you already chose. When you cannot name that
node, recall answers the other question: **where in the semantic tree does this
topic live?** It is a locator, not an answer — it returns no fact, no score, no
distance, no rank, and not the text it matched.

```sh
memora query --input '{"parameters":{"named":{"q":"存储引擎","limit":5}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "RECALL FROM work MATCH :q LIMIT :limit"
memora query --input '{"parameters":{"named":{"q":"存储引擎","limit":5}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "RECALL FROM work IN notes MATCH :q LIMIT :limit"
```

Each hit carries `database`, `table`, `kind`, an optional `object_id`, `path` —
the root-first segments, each with the `route_id` navigation needs — and `arms`,
which says which retrieval arm found **that position**: `["keyword","vector"]`
when both did, `["keyword"]` or `["vector"]` when only one did. Continue
exactly as after discovery: `OPEN ROUTE` the last segment, then `SELECT` the fact.
Recall never prefetches: it does not open the leaf, cache the row, or substitute
for the `SELECT` that produces the answer.

**`arms` is provenance, not strength — and `["vector"]` is the one to be careful
with.** A keyword hit is a word match you can check by eye; a position only the
vector arm returned is a *locator* the words did not confirm. That distinction
matters most for short queries: a bare two-character Chinese word embeds weakly,
so the vector arm's tail is often noise. Measured on a real library, the genuinely
relevant records for one two-character query sat at cosine 0.34–0.37 while
unrelated short records sat at 0.27–0.28 — the engine has no threshold and returns
no score, so those positions are in the list and look like the rest. Read the
`arms` list, and when a position is `["vector"]`-only, open it and read the Row
before you call it relevant. Still nothing to threshold on: order, `LIMIT` and
`truncated` do not depend on `arms`.

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
memora exec --input '{"parameters":{"named":{"v":"<base64>","unit":42,"model":"text-embedding-v4","hash":"<the content_hash SHOW PENDING VECTORS gave you, bare 64 hex>"}},"mutation":{"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-9","reason":"attach embedding"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "ACCEPT VECTOR :v FOR UNIT :unit IN DATABASE work MODEL :model HASH :hash"
```

A batch is several statements in one request, and `--input` takes **one object per
statement as an array** — one `ACCEPT VECTOR`, one input, in source order:

```sh
memora exec --input '[{"parameters":{"named":{"v":"<b64>","unit":42,"model":"text-embedding-v4","hash":"<content_hash of unit 42>"}},"mutation":{"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-9","reason":"attach embeddings"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}},{"parameters":{"named":{"v":"<b64>","unit":43,"model":"text-embedding-v4","hash":"<content_hash of unit 43>"}},"mutation":{"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-9","reason":"attach embeddings"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}]' "ACCEPT VECTOR :v FOR UNIT :unit IN DATABASE work MODEL :model HASH :hash; ACCEPT VECTOR :v FOR UNIT :unit IN DATABASE work MODEL :model HASH :hash"
```

Your provider may cap how many texts one embedding request may carry; that is the
host's business, not the language's (`MEMORA_EMBEDDING_BATCH` declares the cap, and
the CLI splits a refused batch on its own).

`unit` and `HASH` both come from that same `SHOW PENDING VECTORS` row — `unit_no`
is the unit, and `HASH` is its `content_hash` **as printed, the bare 64 hex
characters** (a `sha256:` prefix is a different string and is refused). It is the
hash of the text you embedded, not of the Row: the engine recomputes it and
refuses a mismatch, so an embedding of a previous revision cannot land on the
current one. A unit the engine has no vector for simply stays not-ready —
`RECALL` reports it, and nothing pretends it was attached.

### The fast path: both arms, one statement

The engine never computes a query embedding — the model Provider is the host's —
so the Skill carries the half that turns the user's words into the parameter
`NEAREST` takes:

```sh
echo '{"text":"<what the user asked about>"}' \
  | python3 "<skill-directory>/scripts/embed_query.py"
# -> {"vector":"<base64>","model":"text-embedding-v4","dimensions":1024,"elapsed_ms":232}

memora query --input '{"parameters":{"named":{"q":"<the same words>","n":"<that vector>","limit":10}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' \
  "RECALL FROM work MATCH :q NEAREST :n LIMIT :limit"
```

That one statement runs both arms and the engine fuses them by **rank**
(reciprocal rank fusion, `k = 60`): each arm brings its own order — the vector arm
by distance, the keyword arm by BM25 — a position both arms found outranks one
only a single arm found, ties fall back to table then path, and `LIMIT` truncates
the fused listing. No score, distance or rank is ever returned, so do not look for
one and do not treat the order as a confidence measure. Only the keyword arm
(`MATCH :q`) needs no provider at all.

**When there is no provider, say so.** `embed_query.py` exits `2` and names what is
missing; `MEMORA_EMBEDDING=off` does the same deliberately. Then the recall you
can run answers with `arms: ["keyword"]`, and the honest description of that
answer is "keyword only", never "fused". A host that reports a keyword list as a
two-arm recall has told the user something untrue about how thoroughly it looked.
The same silence applies one level down: `vectors_not_ready` on a result means the
vector arm could not answer for those units, so the listing is real but bounded.

The Database needs a vector **identity** before any of this works: with no vector
ever attached it answers `database has no vector identity yet` — accept one vector
first (below), then search by position. The vector travels as **base64 (raw
URL-safe) of little-endian float32, no padding**, and must match the Database's
locked width; a vector of the wrong width, or one carrying NaN, is refused rather
than rounded. If either arm cannot answer, the fused statement fails rather than
quietly returning the half it could.

### When the fast path is not enough

Recall is a locator, and its result decides **what to read next**, never whether
you have read enough. Three signals — all of them stated by the engine, none of
them a judgement about relevance — mean the answer needs the tree or a census:

- **`vectors_not_ready` on the result**: the vector arm was absent for those
  units, so this listing is bounded. Do not present it as a search of everything.
- **`truncated: true` across several parents**: the hits span two or more
  different parents and there were more. A truncated cross-topic listing is the
  one shape where the right position may simply not be in it — walk the tree or
  census the Table.
- **the question is universal or counting** ("how many", "all of them", "is there
  any"): recall can never answer that. Census the Table.

`arms` is **provenance, not strength**: a position found only by the keyword arm
does not mean the vector arm judged it irrelevant, and none of these three signals
may be replaced by "the results look weak". Reading the Row is not the fallback
for a weak list — it is the only step that turns a position into a fact, every
time.

When the requirement is **structural** rather than similar ("which of my
internships", "where do we keep the decisions about X"), the walk in
[`references/jev-tree.md`](jev-tree.md) is the other way to locate: it takes a
requirement across Databases, Tables and the tree in one call and returns the
positions it committed to. It is slower than this recall and says how it decided;
use it when a reproducible, explainable path is worth seconds.

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
