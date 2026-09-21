---
name: memora
description: memora 是用户的个人记忆与知识库。用户问到关于自己、项目、过往经历或个人相关的问题时，先在 memora 中查找；聊天中出现值得记录的新事实、决定、想法或任何有意义的内容时，存入 memora。本地查不到答案时再上网搜索；搜索到值得保留的内容也存入 memora。
---

# Memora Canonical Skill

Use this single source for stable host behavior. It targets `memora.msql.ast/v1`
and consumes `memora.result/v1`. Keep live schemas, routes, candidates, and rows
out of this file; discover them from the current instance for each task.

Only use the `memora doctor`, `memora query`, `memora exec`,
`memora mutate`, and `memora schema` interfaces for normal database work.
Never inspect, edit, copy, or infer state from physical database, index, journal,
page, or instance files. Logical MSQL results are the only source of database
truth available to the host.
For every Database-specific `query` or `exec`, include one
`memora.authorization/v2` object in `--input`, binding the host actor, exact
user-authorized Database names or stable IDs, and an explicit `default_level`.
Use L0 for reads and plans, L1 for bounded reversible Row writes, and L2 only
for reviewed structural actions. Use `database_levels` when different Databases
need different ceilings. Never widen scope or level to recover from
`permission_denied`; an approval confirms a reviewed hash and never raises the
granted level.

The model Provider belongs to the host, not Memora. An OpenAI-compatible host
may use any user-configured compatible base URL and model, including a Kimi
endpoint exposed through CC Switch; it must never assume `openai.com`. A Claude
Code host may likewise use its CC Switch/Anthropic-compatible configuration.
Never pass Provider base URLs, API keys, or bearer tokens to `memora`, its
database, logs, receipts, exports, or command input.
For controlled real-model evaluation, the adjacent `host-contract.json` fixes
one host-independent natural-language Task, Database scope, and budget. Codex
and Claude Code execute that same Task; Kimi is recorded only as a host-managed
Provider profile, never as a separate Memora protocol or Skill.

## Product manual and Admin

When the user asks what Memora is, how the architecture works, how a read/write
flows through the engine, or how to use/troubleshoot the local Admin, read
[`references/product-manual.md`](references/product-manual.md). It is the
stable product and operations guide; it must not be used as a substitute for
live MSQL discovery. Admin is a local, read-only observer on `127.0.0.1:3888`;
all facts and all mutations still come from the scoped daemon through MSQL. If the
instance's daemon is not running, the CLI starts it and says so on stderr — one
daemon per instance, so a running one is never restarted. You do not need to run
`daemon start` yourself before a query.

## Install once

Before the first Memora operation in a session, resolve this Skill's directory
and run its read-only detector:

```sh
/bin/sh "<skill-directory>/scripts/check.sh"
```

If it reports `ready`, use the detected executable. If it reports `missing`,
do not download or install anything yet. Tell the user that the latest Memora
release is available from `https://github.com/HW-Yue/Memora/releases/latest`
(the verified installer resolves the newest stable tag automatically), show the
default binary destination `~/.local/bin/memora` and the user-level Instance
destination, then ask whether they want to download it manually or explicitly
authorize this Skill's verified installer. If it reports `unhealthy`, show the
bounded diagnostic and ask before replacing anything.

Only after explicit installation authorization, resolve this Skill's own
directory and run:

```sh
/bin/sh "<skill-directory>/scripts/install.sh" --yes
```

The bootstrap supports only macOS arm64/amd64. It resolves the newest stable
GitHub Release by default, verifies the exact SHA-256 entry and staged binary
version, and replaces an old binary only after verification. Pass
`--version MAJOR.MINOR.PATCH` to pin a specific release instead. A checksum,
archive, or version mismatch is a hard failure and must never fall back. Only
an unavailable Release may fall back to a fixed Go module tag or an explicit
local source directory. Do not ask for sudo, change the install script, bypass
`--yes`, or claim success until its idempotent init, daemon start, and doctor
checks finish. If offline without a local source tree and Go toolchain, report
the recoverable blocker.

## Discover

Start a new task or stale Route Frame with bounded discovery. Inspect databases,
then the selected schema and Router. Reuse an existing semantic scope when it
fits; do not invent a table from a name alone.

When the host does not yet know a Database name — a cold Instance, a new task
with no user-named Database, or an expired Route Frame — discover names first.
`SHOW DATABASES` without an `authorization` object is discovery mode and
returns every Database. Supplying an `authorization` object switches it to a
filter that silently drops Databases outside that scope, so a guessed or
placeholder name can hide the real catalog. Bind authorization only after the
user has named a Database; never widen or invent the scope.

The install detector, the health check, and the unauthenticated catalog read
are independent and their error envelopes are small, so run them together in
one turn instead of waiting between them:

```sh
/bin/sh "<skill-directory>/scripts/check.sh"
memora doctor
memora query "SHOW DATABASES LIMIT 32 COMPACT"
```

Show the discovered Database names and purposes to the user and ask which one
to use before the first authorized read or write. Once the user names a
Database, continue the bounded discovery below with that exact name:

```sh
memora query --input '{"parameters":{"named":{"limit":64,"bytes":8192}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW CATALOG ATLAS LIMIT :limit BYTES :bytes COMPACT"
memora query --input '{"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW TABLES FROM work COMPACT"
memora query --input '{"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "DESCRIBE TABLE work.notes COMPACT"
memora query --input '{"parameters":{"named":{"cursor":"","limit":12}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTES FROM TABLE work.notes AT ROOT CURSOR :cursor LIMIT :limit"
```

## Speculative discovery

Use `memora.speculative-discovery/v2` when a new question can benefit from
fewer model continuations. In the same model turn, dispatch independent bounded
calls for one flat Catalog Atlas page and at
most two root Route prefetches from the current same-topic Route Frame. Run the
independent calls in parallel when the host supports it; do not wait for a model
decision between their millisecond-scale results.

Use this profile for at most 32 exact authorized Databases. The Atlas page has
at most 64 entries and 8,192 UTF-8 row JSON bytes. Prefetch
at most two Table roots with at most 12 Routes each, issue at most 10 tool calls,
and keep the total working context within 12,000 UTF-8 bytes. Record topic ID,
exact calls, output bytes, truncation, catalog revision
and each root page snapshot.

Track Atlas snapshot, pages, entries seen, `complete`, and next cursor. If
coverage is partial, follow the cursor without asking the model to choose a
Database. Do not claim a cold Database/Table is absent until coverage is
complete.

Locate Rows with `SHOW ROUTES` (the Agent's main path) and `SELECT` for facts.
Keyword and vector recall are additional product paths still to implement.
jev is an optional Skill-side chooser on the same layer-by-layer surface.

Treat Route results as `navigation_only`. They are neither answers nor evidence.
Explicitly choose one or more Tables from the compact Atlas. For a selected
Table, issue the ordinary Router root and continue the normal layer-by-layer
state machine.

Answer only from revision-matched SELECT rows after normal Route navigation and
RowID lookup.

## Query and summarize

Compare the user's intent with the bounded Route descriptions returned by each
call. Choose a node explicitly, request only its immediate children, and repeat
until a leaf is reached. Every leaf locates at most one active Row, and
`OPEN ROUTE` returns only that Row's locator; never answer from the locator.
Select projected semantic fields by Row ID, then summarize only the returned
Row. Every SELECT Row already carries its own `route_paths` — the full
semantic-index path of the single leaf that locates it — so the host need not
reverse-resolve membership after the fact. Report empty, stale, or
permission-limited results instead of inventing a fallback.

Use this bounded state machine:

```text
SHOW CATALOG ATLAS → deterministic continuation if partial → DESCRIBE TABLE
→ SHOW ROUTES FROM TABLE ... AT ROOT
→ choose one node → SHOW ROUTES UNDER ... (repeat as needed)
→ OPEN ROUTE on a leaf
→ validate database/table/Row/revision locators
→ SELECT projected fields + row_id + revision
→ answer only from revision-matched SELECT rows
```

Do not synthesize query terms, similarity scores, or a full path. Select one
layer from the descriptions actually returned by the database. Do not broaden
a permission denial. If a selected Row changed, discard it and refresh discovery
at most once when it can materially affect the answer.

```sh
memora query --input '{"parameters":{"named":{"parent":"route_architecture","limit":12}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTES UNDER :parent LIMIT :limit"
memora query --input '{"parameters":{"named":{"leaf":"route_storage","limit":1}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "OPEN ROUTE :leaf LIMIT :limit"
memora query --input '{"parameters":{"named":{"row":"row_01","limit":10}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SELECT title, summary, row_id, revision FROM work.notes WHERE row_id = :row LIMIT :limit"
```

Read the current `query_budgets` row before navigation. The bundled ceilings are
12 Router rows, one locator per opened leaf, 10 selected rows across explicitly
chosen leaves, and 12,000 context characters. `open_locators` is retained as a
compatibility budget but cannot raise a leaf above its `0..1` cardinality. Use
the smaller current limits for the remaining budgets. A locator cursor is never
expected from a valid leaf. Drop the Route Frame when its schema or route
revision is stale, the topic changes, or the task ends.

Stop when enough SELECT evidence answers the question, all candidates are
exhausted, a hard budget is reached, access is denied, or another call cannot
change the answer. Cite `database.table`, Row ID, revision, and available source
anchor for every factual summary. Distinguish “no matching Row,” “truncated,”
“stale during SELECT,” and “permission denied.”

## Recall a position you cannot name

`SHOW ROUTES` walks down from a node you already chose. When you cannot name that
node, recall answers the other question: **where in the semantic tree does this
topic live?** It is a locator, not an answer — it returns no fact, no score, no
distance, no rank, no reason, and not the text it matched.

```sh
memora query --input '{"parameters":{"named":{"q":"存储引擎","limit":5}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "RECALL FROM work MATCH :q LIMIT 5"
memora query --input '{"parameters":{"named":{"q":"存储引擎","limit":5}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "RECALL FROM work IN notes MATCH :q LIMIT 5"
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
once (`MATCH :q NEAREST :v`) merges what each found, counts a position found
twice once, and still truncates by that same rule; if either arm cannot answer,
the statement fails rather than quietly returning the half it could.

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

`LIMIT` is required and bounded to 1–1000; the query must be at least 3
characters, and a shorter one is refused rather than silently returning nothing.
Hits are de-duplicated by path and ordered by table then path, so the same query
over an unchanged database returns the same list. Scope is one Database, with an
optional `IN <table>`. Today the lexical path is the only one wired; when a
position exists but no hit returns, treat it as "not matched", never as "the tree
has nothing there", and always report the query you used.

## Let jev choose a layer (optional)

Walking the tree means picking a child at every layer. You can make that choice
yourself — that is the main path — or, when the host has a TypeSafe key, hand one
layer to jev and take back which child to descend into. It is the fourth
retrieval path and the only optional one: skip it and the other three are
unchanged, because the kernel still answers `SHOW ROUTES` and this only decides
which answer to follow.

```sh
echo '{"intent":"我上次说那个数据库的崩溃恢复是怎么做的","options":[{"name":"技术","purpose":"技术决策与实现"},{"name":"生活","purpose":"健康、阅读与日常"},{"name":"工作","purpose":"项目与求职进展"}]}' | python3 scripts/jev_select.py
```

It answers with `{"choice":"…","confidence":…,"probabilities":{…}}`.

Two rules that matter more than the call itself:

- **Hand over names and purposes only.** Never send route ids: an identifier is
  not an authorization token and has no business in a model prompt. The script
  drops anything else it is given, but do not pass it in the first place.
- **`confidence` is concentration, not correctness.** It says how firmly jev
  preferred one option; it cannot say whether the right child is in this layer at
  all. Read a low value as "ask the user or decide yourself". Measured on this
  repository's own tree: a clear intent came back `1.0`, a deliberately vague one
  (`嗯……那个东西`) came back `0.58` — treat that neighbourhood as unresolved.

The fallback is mechanical if you want it to be: pass `--min-confidence 0.8` and a
weaker answer comes back as exit `5` with its confidence, so you choose the child
yourself or ask the user instead of acting on a coin flip.

Exit codes tell you what happened: `0` answered, `2` no key configured (choose
yourself — this is not an error), `3` the provider refused or answered something
that is not one of the options, `4` the request was malformed, `5` the answer was
below `--min-confidence`. `--dry-run` prints
the request without sending it, which is the cheap way to see what would travel.

## Decide where knowledge lives

Before persisting a new piece of knowledge, decide where it belongs. Decide
from large to small scope and only create when reuse is impossible:

1. Reuse an existing Database whose purpose/scope clearly covers the user's
   topic and whose anti_scope does not exclude it.
2. Create a new Database only for a genuinely new domain — the user's first
   mention of a personal topic with no matching Database warrants a new
   Database, written before anything else, with an explicit purpose and scope.
3. Inside the chosen Database, reuse an existing Table whose purpose and
   row_semantics fit the knowledge; add a Row there.
4. Create a new Table only when no existing Table fits and the content is a
   distinct, recurring kind the user will keep adding to.
5. Never create on a hunch or from a name alone: match by the object's declared
   purpose/scope and semantic description, not by guessing equivalence.
6. Every new Table MUST declare exactly one Column with `ROLE 'summary'`, and
   its TEXT ceiling must hold a ~1,000-CJK-character Markdown document plus
   syntax (for example `TEXT(2500)`; the 1,200 default is too small). A Table
   without a `summary` Column cannot hold a displayable Row. Declare
   `ROLE 'title'` as well when the Table needs a short label.

## Write

Within the user's authorized scope, use:

```text
Discover → query existing rows → plan → validate → execute → verify
```

Choose IGNORE, INSERT, REVISE, MERGE, SPLIT, or MOVE before generating
MSQL. Prefer revising an existing semantic module over appending a duplicate.
Use parameters, expected schema/revision, a maximum affected-row count, actor,
source, reason, and the complete current Route leaf membership snapshot.
Keep transactions short and verify the returned revision and logical row.

Every INSERT and every UPDATE that creates or replaces a semantic module MUST
write the `summary` Column. `summary` is the Row's body: a complete,
self-contained Markdown document of roughly 1,000 CJK characters that a reader
can understand without opening anything else. It is not a one-line abstract,
not a bullet list, and not a restatement of `title`. A Row without a usable
`summary` is not a usable memory — never write one and never leave `summary`
empty to "fill in later". If the configured TEXT ceiling cannot hold the
document, submit a Schema change to widen the Column first (see
"Evolve schemas"); never silently truncate.

Build one `memora.mutation-plan/v1` object. Every decision includes at least one
read-only preflight with explicit Row expectations. IGNORE has no steps. INSERT,
REVISE, and MOVE have one step; MERGE is one UPDATE plus DELETE steps;
SPLIT is one UPDATE plus INSERT steps. Keep at most eight steps. Every INSERT or
UPDATE supplies the complete `route_leaf_ids` snapshot naming exactly one leaf:
a Row occupies exactly one Leaf, and a Leaf holds at most one live Row.
A Row with no Route membership can never be reached by semantic navigation, so
an empty array is not a valid snapshot: attach an existing empty leaf, or create
the leaf first.

### Create the Route leaf you are about to write into

Discovery statements (`SHOW ROUTES`, `OPEN ROUTE`) only navigate an existing
tree. Creating the semantic index itself uses `CREATE ROUTE`, which runs at
risk level **L2** — the L1 level used for Row writes is refused:

```text
CREATE ROUTE ROOT FOR TABLE <db>.<table> PURPOSE :purpose [SYNOPSIS :synopsis]
CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose [SYNOPSIS :synopsis]
```

`KIND` is `'branch'` for a grouping node and `'leaf'` for a node that locates a
Row. Both forms return the new `route_id`. A Row write mounts on exactly one
leaf, and it says so one of two ways — never at the top level of the request,
always inside `mutation`:

- `route_path`: name the path and let the engine reuse or create the segments.
  This is the one the INSERT example below uses, and the one to prefer when the
  leaf may not exist yet.
- `route_leaf_ids`: hand over the `route_id` you just created, as the UPDATE
  example does.

A Table needs its root once, then one leaf per Row.

A leaf holds at most one live Row, so a new Row needs its own leaf: check the
target leaf is empty with `OPEN ROUTE`, and create a sibling when it is taken.
When a parent already carries `route_policy.branch_fanout` live children the
create fails; decide between restructuring the subtree and raising the limit,
which moves by at most 4 per change.

### Quote rules that `memora parse` will not catch

`PURPOSE`, `NAME` and `KIND` must be a **string literal in single quotes** or a
**named parameter**. Two spellings parse cleanly and then fail at execution:

| Written | Parsed as | Execution result |
| --- | --- | --- |
| `PURPOSE "root navigation"` | quoted identifier | `Router purpose must be a literal or parameter` |
| `KIND LEAF` | bare identifier | `Router kind must be a literal or parameter` |
| `PURPOSE 'root navigation'` | string literal | accepted |
| `KIND :kind` with `"kind":"leaf"` | parameter | accepted |

Double quotes mean *identifier*, not string. A successful `memora parse` only
proves the shape is grammatical; the executor validates types separately, so
parse success is not permission to skip a real execution check.

**Prefer named parameters for every dynamic value and every enum**, including
`KIND`. That keeps user text out of the statement and removes the whole class of
parser/executor mismatch above.

### Bootstrap a Router on a Table that has none

```sh
# 1. Confirm the Table really has no root yet — an empty rows array means none.
memora query --input '{"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12"

# 2. Create the root (L2, parameterised).
memora exec --input '{"parameters":{"named":{"purpose":"Semantic navigation root for work notes"}},"mutation":{"expected_schema_version":1,"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-7","reason":"bootstrap router"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L2"}}' "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose"

# 3. Create a branch, then a leaf under it, reusing the returned route_id.
memora exec --input '{"parameters":{"named":{"parent":"route_root","name":"architecture","kind":"branch","purpose":"Architecture decisions"}},"mutation":{"expected_schema_version":1,"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-7","reason":"group architecture notes"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L2"}}' "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose"

# 4. Verify: the child appears under the parent, and the leaf is still empty.
memora query --input '{"parameters":{"named":{"parent":"route_root"}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTES UNDER :parent LIMIT 12"
memora query --input '{"parameters":{"named":{"leaf":"route_leaf"}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "OPEN ROUTE :leaf LIMIT 1"
```

This bootstrap is ordinary Router construction, not a Route mutation plan.
`PLAN ROUTE MUTATION` only restructures an existing tree (SPLIT, MERGE, MOVE);
it cannot create the first root, and it is not the path for adding a leaf to
hold a new Row.

### Or name the path and let the kernel complete it

An INSERT may carry `route_path` instead of `route_leaf_ids`: one entry per
segment, each with its own `name`, `kind` and `purpose`. The kernel reuses the
segments that already exist and creates the ones that do not, in the same
transaction as the Row. The two options are mutually exclusive, and `route_path`
is accepted by INSERT only.

```sh
memora exec --input '{"parameters":{"named":{"title":"Use SQLite"}},"mutation":{"expected_schema_version":1,"max_affected_rows":1,"route_path":[{"name":"architecture","kind":"branch","purpose":"Architecture decisions"},{"name":"sqlite","kind":"leaf","purpose":"Why SQLite"}],"actor":"agent:host","source":"conversation:event-7","reason":"record the decision"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "INSERT INTO work.notes (title) VALUES (:title)"
```

Sibling names match case-insensitively and never by alias. Expect a refusal when
the Table has no root yet (create it explicitly — its purpose is table-level
semantics), when an interior segment is a leaf, when the last segment is a
branch, when the leaf exists under a different purpose, or when it already holds
a live Row. Nothing is created unless the whole write commits.

Before attaching a new Row, verify that the target leaf is empty;
an occupied leaf requires a new semantic leaf, because a Row occupies exactly one
leaf and cannot also be reached through a second one. Submit the plan through `mutate` so
Policy validation occurs before any Tool call and multi-step changes share one
short transaction.

```sh
memora exec --input '{"parameters":{"named":{"row":"row_01","summary":"<complete self-contained ~1,000-CJK-character Markdown document; abbreviated in this example>"}},"mutation":{"expected_schema_version":1,"expected_revision":2,"max_affected_rows":1,"route_leaf_ids":["route_query"],"actor":"agent:host","source":"conversation:event-7","reason":"refine verified conclusion"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "UPDATE work.notes SET summary = :summary WHERE row_id = :row"
memora mutate --plan '{"version":"memora.mutation-plan/v1","id":"plan-7","decision":"IGNORE","database":"work","table":"notes","actor":"agent:host","source_event_id":"conversation:event-7","reason":"existing Row already captures it","authorized_databases":["work"],"preflight":[{"id":"duplicate-check","msql":"SELECT row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1","input":{"parameters":{"named":{"row":"row_01"}}},"expect_rows":1}],"steps":[],"verify":[]}'
```

## Evolve schemas

Before creating a domain, discover existing Database and Table names and aliases.
Submit the proposed name plus a short explicit synonym set through one
`memora.schema-plan/v1` ensure plan. Database purpose/scope, Table
purpose/row_semantics, and every Column type/purpose are mandatory. Reuse an
exact candidate or alias; do not infer equivalence from a name alone.

For an existing Table, do not translate Column evolution into ad hoc ALTER
statements or the older host rename runner. Inspect the exact Table/Column IDs and
revisions, then submit explicit ADD_COLUMN, RENAME_COLUMN, ALTER_COLUMN, or
DROP_COLUMN intent as `memora.schema-change-proposal/v1` through read-only MSQL:

```sh
memora query --input '{"parameters":{"named":{"proposal":{"version":"memora.schema-change-proposal/v1","proposal_id":"schema-proposal-9","actor":"agent:host","source_event_id":"conversation:event-9","reason":"tighten reviewed title budget","expected_table_revision":4,"changes":[{"change_id":"title-budget","action":"ALTER_COLUMN","column_id":"col_title","expected_revision":2,"definition":{"name":"title","type":"TEXT(200)","nullable":false,"purpose":"Decision title","semantic_role":"title"}}]}}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal"
```

The result must be `memora.schema-change-plan/v1`. `review_required` means only
that current values passed the bounded compatibility scan; it is not approval or
execution. `blocked` includes bounded RowID-only blockers and must lead to a new
proposal or explicit Row revisions. A truncated scan is a hard failure. Show the
exact plan and impact, and never execute its actions through ordinary DDL. Ask the
user before any destructive, broad, or constraint-tightening change.

Only after explicit approval, pass the exact unchanged plan through `memora exec`:

```sql
APPLY SCHEMA CHANGE PLAN :plan FOR TABLE work.notes
```

Bind `authorization.approval.action=APPLY_SCHEMA_CHANGE` and
`subject_sha256` to the 64 hexadecimal characters after the plan hash's
`sha256:` prefix. Keep the same authorized Database scope. Require a
`memora.schema-change-receipt/v1` with `status=committed` and `verified=true`;
anything else is not a verified migration. An approval mismatch, changed
Catalog/Row guard, or stale plan requires fresh inspection and planning.

A reversible receipt may include `compensation_proposal`. It is only inverse
intent: submit it through `PLAN SCHEMA CHANGE`, review its new impact, and obtain
a new hash-bound approval before APPLY. Never execute a compensation proposal or
receipt directly. A destructive plan containing DROP has no automatic
compensation proposal because History values must not be presented as an
ordinary reversible Schema action.

## Request the user

Ask the user before any semantic-conflict mutation. Build a temporary
`memora.semantic-conflict/v1` view from one proposal and 1–10 revision-matched
SELECT rows. Show each alternative side by side with actor, source event,
reason, Row ID, revision, and a field-sorted proposal/existing diff. Distinguish
a missing field from a present NULL. The view contains no MSQL or Mutation Plan
and is never stored as a Row, History entry, checkpoint, or event-journal body.

Wait for an explicit user instruction, then create a new
`memora.conflict-resolution/v1` with a new source event. Map `RETAIN` to an
IGNORE Plan, `REWRITE` to a REVISE Plan for the displayed Row/revision, and
`REMOVE` to a MERGE Plan that updates one displayed survivor and logically
deletes only the selected displayed Rows. Bind Database/Table, actor, reason,
authorization, step targets, and expected revisions to the conflict view. Run
the resulting Plan through normal Policy and `mutate`; refresh the
view on a revision conflict. Never expand permission, modify an unshown Row,
create a database-level candidate/disputed state, or silently pick a winner.

Also ask before irreversible, privacy-reducing, permission-expanding, or broadly
destructive operations.

## Recover an archived deletion

A DELETE removes the Row, the leaf it occupied, its history and both ends of its
links. The engine writes one archive record first, and rebuilding from it is your
work, not the engine's.

```sh
memora query --input '{"parameters":{"named":{"row":"row_01","limit":10}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ARCHIVE FROM work.notes FOR ROW :row LIMIT :limit"
memora query --input '{"parameters":{"named":{"archive":"archive_01"}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "OPEN ARCHIVE :archive"
```

`SHOW ARCHIVE` lists metadata only and requires both a Table scope and a LIMIT;
`OPEN ARCHIVE` returns one record in full — the path root-first, and the Row as it
was stored, including the links it carried. A deleted Row is unreachable
everywhere else (`SELECT`, `SHOW HISTORY`, `AS OF`, `OPEN ROUTE`); the archive is
the single exception, and `SELECT` cannot reach it either. Rebuild by recreating
the path (`CREATE ROUTE`, or `route_path` on the INSERT) and mounting the new Row
on its leaf. The archived IDs are a record of what was, not a promise it can be
reused.

## Drain the link repair queue

Linking is two-sided and lazy repairs are queued rather than applied inline: a
reshape queues the links that pointed at it, and an in-place write queues the
summaries that describe it. Draining is an explicit, bounded write.

```sh
memora exec --input '{"parameters":{"named":{"limit":64}},"mutation":{"max_affected_rows":64,"actor":"agent:host","source":"conversation:event-9","reason":"drain link repairs"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "REPAIR LINKS IN DATABASE work LIMIT :limit"
```

`LIMIT` is the batch size and may not exceed `max_affected_rows`. The receipt
reports how many endpoints were repaired, discarded (the queued condition no
longer holds), and how many remain. A repair re-checks every entry before
applying it and never queues a follow-up of its own, so repeating the statement
until `remaining` is zero is safe.

## Router mutations

### Route branch fan-out

One root or branch may carry at most `branch_fanout` live children. The startup
default is 12 and each Database owns its own value:

```sh
memora query --input '{"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW CONFIGURATION ROUTE_POLICY"
```

Exceeding it is not a warning and is never paginated away: the write fails with
`constraint_violation` and `details.reason = route_branch_fanout_exceeded`,
carrying `parent_route_id`, `live_children`, `branch_fanout`, and two executable
remedies. Choose between them yourself; do not retry the same write.

1. `restructure_subtree` — the crowded parent has no clear grouping left, and the
   new node belongs inside an existing child, or the children should be regrouped.
   Use the Route mutation proposal flow below.
2. `raise_branch_fanout` — the domain genuinely has more sibling groups than the
   current limit, and merging them would lose a real distinction. Raise this
   Database's limit with an expected revision, actor, and reason:

```sql
ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :fanout
```

Prefer restructuring when the crowded children share an obvious parent concept;
prefer raising the limit when they are genuinely parallel and already
distinguishable by name and purpose alone. Lowering the limit never invalidates
an existing tree — it only refuses further growth — so a Database that already
sits above its limit stays readable and maintainable.

For a local Router split, merge, or move, inspect the exact current nodes and leaf
locators first. Express the semantic names, purposes, source revisions, and complete
child Route or RowID grouping in `memora.route-mutation-proposal/v1`; do not ask the
engine to infer the grouping. Generate a review-only plan through MSQL:

```sh
memora query --input '{"parameters":{"named":{"proposal":{"version":"memora.route-mutation-proposal/v1","proposal_id":"route-proposal-1","operation":"MOVE","actor":"agent:host","source_event_id":"conversation:event-9","reason":"move reviewed subtree","sources":[{"route_id":"route_source","expected_revision":3}],"target_parent_id":"route_archive"}}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal"
```

Verify that the result is `memora.route-mutation-plan/v1`, `status=review_required`,
and has base snapshot and plan hashes. Show the exact plan and impact to the user.
Only after explicit approval, submit that unchanged plan through `memora exec`; bind
the generic approval to action `APPLY_ROUTE_MUTATION` and to the 64 hexadecimal
characters after the plan hash's `sha256:` prefix:

```sql
APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes
```

Use `memora exec` with `parameters.named.plan` set to the exact reviewed object.
Set `authorization.approval.version=memora.approval/v1`,
`authorization.approval.action=APPLY_ROUTE_MUTATION`, the matching
`subject_sha256`, and `confirmed=true`; keep the same authorized Database scope
and set its level to L2.

Require `memora.route-mutation-receipt/v1`, `status=committed`, and `verified=true`.
Never translate plan actions into ad hoc CREATE/DELETE/UPDATE statements. Never edit
and re-hash a reviewed plan. A truncated scan, approval mismatch, or revision conflict
requires a fresh inspection and new proposal; do not retry a stale plan.

## License

Memora is free for uses allowed by the
[PolyForm Noncommercial License 1.0.0](https://polyformproject.org/licenses/noncommercial/1.0.0).
Commercial use requires a separate written, paid commercial license.

Required Notice: Copyright 2026 HW-Yue. Commercial use requires a separate paid commercial license from the copyright holder. Commercial licensing inquiries: https://github.com/HW-Yue/Memora

## Return a receipt

After a mutation, return a receipt under 2,000 characters with the logical
objects changed, action, revision/commit sequence, reason/source, verification
result, warnings, truncation, and any required follow-up. After a read, cite the
database/table/Row IDs used and distinguish missing data from denied or truncated
data. Never claim success from an error envelope or incomplete source coverage.
