---
name: memora
description: memora 是用户的个人记忆与知识库。用户问到关于自己、项目、过往经历或个人相关的问题时，先在 memora 中查找；聊天中出现值得记录的新事实、决定、想法或任何有意义的内容时，存入 memora。本地查不到答案时再上网搜索；搜索到值得保留的内容也存入 memora。
---

# Memora Canonical Skill

Use this single source for stable host behavior. It targets `memora.msql.ast/v1`
and consumes `memora.result/v1`. Keep live schemas, routes, candidates, and rows
out of this file; discover them from the current instance for each task.

Only use the `memora assimilate`, `memora capture`, `memora decide`, `memora doctor`, `memora query`, `memora exec`,
`memora feedback`, `memora maintain`, `memora mutate`, `memora schema`, and
`memora reflect` interfaces for normal database work. The approval-gated
`upgrade` and `doctor repair` recovery commands below are the only exception.
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
all facts and all mutations still come from the scoped daemon through MSQL.

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

## Upgrade or recover an Instance

Never migrate or roll back an Instance implicitly. If a command reports
`upgrade_required`, run only the read-only plan first:

```sh
memora upgrade --plan
```

Show the user the exact Instance ID, from/to versions, backup destination, and
steps. Only after explicit approval may you run `memora upgrade --apply --yes`.
Do not treat install consent as upgrade consent, and do not auto-approve because
normal database work is blocked.

If a migration journal reports an incomplete migration, show the journal-bound
backup and ask separately before running `memora doctor repair --yes`. Do not
choose an arbitrary backup or add `--backup` unless the user explicitly selected
that verified absolute path. Upgrade apply and doctor repair must remain outside
host-level implicit command permissions.

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
calls for one flat Catalog Atlas page, lexical Route candidates, an optional
vector candidate query, and at
most two root Route prefetches from the current same-topic Route Frame. Run the
independent calls in parallel when the host supports it; do not wait for a model
decision between their millisecond-scale results.

Use this profile for at most 32 exact authorized Databases. The Atlas page has
at most 64 entries and 8,192 UTF-8 row JSON bytes. Across all
predictors allow at most 8 candidates and 4,096 candidate UTF-8 bytes; when both
Lexical and Vector run, allocate 4 candidates and 2,048 bytes to each. Prefetch
at most two Table roots with at most 12 Routes each, issue at most 10 tool calls,
and keep the total working context within 12,000 UTF-8 bytes. Record topic ID,
exact calls, output bytes, truncation, each predictor snapshot/catalog revision
and each root page snapshot. Keep different predictor snapshots separate and
require their Catalog revisions to agree.

Track Atlas snapshot, pages, entries seen, `complete`, and next cursor. If
coverage is partial, follow the cursor without asking the model to choose a
Database. Do not claim a cold Database/Table is absent until coverage is
complete. A predictor may point to a Table outside the current Atlas page; use
normal Router fallback while deterministic Atlas continuation remains available.

Always pass the lexical question as a parameter. Add Vector only when the host
already has a normalized query vector and the exact generation space digest;
split the global predictor budget before issuing either call. Missing encoder,
unavailable/stale generation, zero hits, or a failed prefetch are normal
navigation outcomes, not query failures.

```sh
memora query --input '{"parameters":{"named":{"lexical_query":"crash recovery","lexical_limit":8,"lexical_bytes":4096}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :lexical_query LIMIT :lexical_limit BYTES :lexical_bytes"
```

`SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query` is the full-content inverted index: it returns every object matching the query in one bounded page, with `kind` one of `database | table | column | route | row`. Use it when a keyword must locate both the semantic index (route) and a concrete Row, instead of the route-only `SHOW ROUTE CANDIDATES`. A Row hit returns `database_id/table_id/object_id/revision`; follow it with `SELECT ... WHERE row_id = :row` to read the Row, whose own `route_paths` already carries its semantic path, so membership need not be reverse-resolved.

```sh
memora query --input '{"parameters":{"named":{"query":"crash recovery","location_limit":10,"utf8_byte_limit":8192}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query LIMIT :location_limit BYTES :utf8_byte_limit"
```

Treat every Discovery candidate and prefetched Route as `navigation_only`.
They are neither answers nor evidence, and scores with different kinds are not
comparable. Explicitly choose one or more Tables from the compact Atlas; a
zero-hit Table remains selectable, and partial Atlas coverage is never an
exclusion filter. Reuse a prefetched root only when
its topic, Table, Catalog revision, page snapshot and Route revisions are still
current. For a selected Table without a valid prefetch, issue the ordinary Router root fallback
and continue the normal layer-by-layer state machine.

Discard the speculative Frame when the question has a different topic, a
revision is stale, the context ceiling is crossed, or the task ends. A wrong
prediction may waste bounded context but must never exclude a Table, widen
authorization, persist in a system prompt, or change the visible Row set.
Answer only from revision-matched SELECT rows after normal Route navigation and
RowID lookup.

## Query and summarize

Compare the user's intent with the bounded Route descriptions returned by each
call. Choose a node explicitly, request only its immediate children, and repeat
until a leaf is reached. Every leaf locates at most one active Row, and
`OPEN ROUTE` returns only that Row's locator; never answer from the locator.
Select projected semantic fields by Row ID, then summarize only the returned
Row. Every SELECT Row already carries its own `route_paths` — the full
semantic-index paths of the leaves that locate it — so the host need not
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

Choose IGNORE, INSERT, REVISE, MERGE, SPLIT, MOVE, or RELATE before generating
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
REVISE, MOVE, and RELATE have one step; MERGE is one UPDATE plus DELETE steps;
SPLIT is one UPDATE plus INSERT steps. Keep at most eight steps. Every INSERT or
UPDATE supplies the complete `route_leaf_ids` snapshot with at least one leaf.
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
Row. Both forms return the new `route_id`; pass that id in the Row write's
`route_leaf_ids`. A Table needs its root once, then one leaf per Row.

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

Before attaching a new Row, verify that every target leaf is empty;
an occupied leaf requires a new semantic leaf, while the same Row may still use
multiple distinct leaves. Submit the plan through `mutate` so
Policy validation occurs before any Tool call and multi-step changes share one
short transaction.

```sh
memora exec --input '{"parameters":{"named":{"row":"row_01","summary":"<complete self-contained ~1,000-CJK-character Markdown document; abbreviated in this example>"}},"mutation":{"expected_schema_version":1,"expected_revision":2,"max_affected_rows":1,"route_leaf_ids":["route_query"],"actor":"agent:host","source":"conversation:event-7","reason":"refine verified conclusion"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "UPDATE work.notes SET summary = :summary WHERE row_id = :row"
memora mutate --plan '{"version":"memora.mutation-plan/v1","id":"plan-7","decision":"IGNORE","database":"work","table":"notes","actor":"agent:host","source_event_id":"conversation:event-7","reason":"existing Row already captures it","authorized_databases":["work"],"preflight":[{"id":"duplicate-check","msql":"SELECT row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1","input":{"parameters":{"named":{"row":"row_01"}}},"expect_rows":1}],"steps":[],"verify":[]}'
```

## Capture pending host input

Before deciding whether a new short user assertion or bounded source excerpt is
worth a database mutation, capture it as one `memora.host-input/v1`. Bind a
stable input ID, workspace, actor, and the exact 1–32 user-authorized Database
selectors. Keep `candidate_text` within 12,000 UTF-8 bytes. This auxiliary inbox
is temporary handoff state, not a semantic Row, fact, History entry, or answer.

Use `conversation_assertion` only without a locator or source hash. A
`document_anchor` or `repository_anchor` requires both a bounded locator and the
source content SHA-256. Never label capture as `reviewed_source`. Send a whole
document, directory, media source, or multi-window task through `assimilate`
instead of splitting it into Host Inputs.

```sh
memora capture --candidate '{"version":"memora.host-input/v1","input_id":"input-12","workspace":"project-memora","actor":"agent:host","authorized_databases":["work"],"candidate_text":"Router results are locators, not facts.","source":{"kind":"conversation_assertion","title":"Router boundary"}}'
```

Require `memora.host-input-receipt/v1`, `status=pending`, and matching input,
content, and scope hashes. The capture receipt deliberately omits candidate
text. After host restart or context loss, reload the exact pending candidate
only with its workspace:

```sh
memora capture --receipt input-12 --workspace project-memora
```

An identical input ID/content replay is success; different content under the
same ID is a hard revision conflict. `pending` proves only durable capture. Do
not infer IGNORE/WRITE/REVISE or run MSQL from the receipt; the worthiness
decision is a separate reviewed step.

## Finalize worthiness

After capture, use normal discovery and bounded queries to decide whether the
candidate should be ignored, inserted as a new semantic module, or used to
revise an existing Row. Express and execute that choice through a validated
Mutation Plan first. Then finalize the pending input with one strict
`memora.worthiness-decision/v1`:

```sh
memora decide --decision '{"version":"memora.worthiness-decision/v1","decision_id":"decision-12","input_id":"input-12","workspace":"project-memora","actor":"agent:host","input_sha256":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","scope_sha256":"sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789","verdict":"IGNORE","reason":"preflight found the same semantic module","authorized_databases":["work"],"mutation_receipt":{"version":"memora.mutation-receipt/v1","plan_id":"plan-ignore-12","decision":"IGNORE","status":"ignored","changes":[],"ignored":1,"verified":true,"warnings":[]}}'
```

IGNORE requires the verified ignored receipt from an IGNORE Mutation Plan.
WRITE requires a committed, verified INSERT receipt; REVISE requires a
committed, verified REVISE receipt. WRITE/REVISE also name the authorized
Database/Table and the exact Row ID/revision returned by one matching change.
Never fabricate a Mutation Receipt and never finalize from
`committed_unverified`; resolve verification first.

Require `memora.worthiness-receipt/v1` and `status=finalized`. It omits the
candidate text. After restart, reload the stable decision with:

```sh
memora decide --receipt decision-12 --workspace project-memora
```

The engine verifies receipt shape and capture binding, not semantic truth. A
finalized WRITE/REVISE refers to the preceding MSQL mutation; the decision API
does not write Rows itself.

## Assimilate sources

Treat documents and media as temporary host input. Start one
`memora.assimilation-event/v1` inventory with a source ID, bounded title/locator,
content SHA-256, and parent-linked source, directory, chapter, page, table, and
attachment units. Give each readable unit a normalized half-open extent; mark a
unit optional only when omission is intentional. Do not place source text in a
label, locator, anchor, event, or database Row.

Read bounded windows and send only unit ID, `[start,end)`, and window SHA-256.
Memora merges overlaps and treats an identical window as a no-op. Save an active
unit, offset, bounded host cursor, and last window event before interruption.
Use a `status` event after restart or context loss to recover the checkpoint and
unread ranges; do not depend on old chat history.

Call `finish` only after inventory traversal. An `incomplete` receipt is a hard
failure: continue from its unread ranges and never report successful absorption.
`coverage_complete` means only that F36 review and semantic submission may
begin; it does not mean knowledge was written. After a successful later commit
or explicit cancellation, call `clear` to remove temporary Memora state. Never
delete or modify the user's source file.

Build one `memora.assimilation-submission/v1` only after coverage completes.
Represent each complete, independently editable semantic module with its normal
Mutation Plan; represent structure only with RELATE Plans. Bind every module and
relationship to at least one short source anchor inside a readable inventory
unit. Express RELATE endpoints as reviewed module IDs in the `source` and
`target` parameters; Memora replaces them with the verified object IDs returned
by those module plans. Bind every important number or other key fact separately to its module,
field, value SHA-256, and exact anchor. Do not copy source windows or quotations
into the submission merely to support review.

Each semantic module's `summary` is a complete, self-contained Markdown
document of roughly 1,000 CJK characters — the full rendered body the reader
should see, not a compressed extract or a few bullet points. Configure the
summary Column's TEXT limit (e.g. `TEXT(2500)`) to hold the document plus
Markdown syntax; the 1200-character default ceiling is too small for a
1,000-CJK-character body. Length is counted in Unicode code points, so Markdown
headers, list markers, and code blocks consume the same budget as CJK text.
Never silently truncate: if the ceiling is too low, submit a Schema change to
widen the summary Column before writing, and write the document to match the
configured budget.

Run a second pass as `memora.assimilation-review/v1`. It may use another Agent,
or the same Agent with a context ID isolated from the draft. It must bind the
draft SHA-256 and coverage revision, check the exact module/relationship/key-fact
ID sets, and explicitly verify anchors, key facts, conflicts, and absence of raw
source content. If any semantic conflict remains, submit its ID and stop on
`needs_user`; resolve it through the normal conflict flow before creating a new
submission ID.

Only `committed` in `memora.source-receipt/v1` means absorption succeeded. An
`in_doubt` submission may have partially committed: query the affected logical
Rows and revisions, then recover with a new submission instead of replaying the
old write. Reload compact provenance with `memora assimilate --receipt <id>`.
After committed, send an explicit coverage `clear` event; the Source Receipt
survives while the temporary inventory, coverage, windows, and checkpoint do not.

```sh
memora assimilate --event '{"version":"memora.assimilation-event/v1","event_id":"book-status-2","task_id":"book-task","workspace":"project-memora","kind":"status"}'
memora assimilate --receipt book-submit-1
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

```sh
memora schema --plan '{"version":"memora.schema-plan/v1","id":"schema-8","actor":"agent:host","source_event_id":"conversation:event-8","reason":"new durable project domain","authorized_databases":["work"],"ensure":{"database":{"name":"work","purpose":"Project knowledge","scope":"Reviewed projects"},"database_synonyms":["projects"],"table":{"name":"notes","purpose":"Durable decisions","row_semantics":"One reviewed decision","columns":[{"name":"title","type":"TEXT(200)","nullable":false,"purpose":"Decision title"}]},"table_synonyms":["decisions"]}}'
```

## Reflect conversation deltas

Call `memora reflect` explicitly when a stable conclusion is ready, the user asks
to remember it, before a host compaction checkpoint, or when the host can signal
session end. Do not assume a hidden lifecycle hook and do not invoke it after
every message. Mark greetings, transient reasoning, and duplicates as `ignore`;
attach one validated Mutation Plan to at most one `persist` delta per event.

Use a host-stable `event_id`, session ID, workspace, and authorized Database set.
The Mutation Plan provenance must equal the event ID and cannot expand that
authorization. Retrying identical content returns the stored receipt without a
Tool call; reusing an ID for different content is a revision conflict. An event
left in progress by interruption is in doubt and requires recovery instead of a
blind retry. A `needs_context` receipt means the host must restore the missing
Database or plan before writing.

Checkpoint events store only active Database, Route path, and last event ID;
they replace the same session's prior checkpoint during project switches.
Session-end events explicitly clear it. Never put raw conversation text in the
event journal or checkpoint.

```sh
memora reflect --event '{"version":"memora.conversation-event/v1","event_id":"checkpoint-9","session_id":"host-session-2","kind":"checkpoint","workspace":"project-memora","authorized_databases":["work"],"checkpoint":{"active_database":"work","route_path":"/architecture","last_event_id":"event-8"}}'
```

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
the resulting Plan through normal Policy and `reflect`/`mutate`; refresh the
view on a revision conflict. Never expand permission, modify an unshown Row,
create a database-level candidate/disputed state, or silently pick a winner.

Also ask before irreversible, privacy-reducing, permission-expanding, or broadly
destructive operations.

## Maintain semantic health

Run `memora maintain --report` only when the user asks or at an explicit
conversation checkpoint; do not assume a hidden hook or scan after every turn.
Treat `memora.semantic-health/v2` issues as deterministic structural candidates, not facts.
Route capacity, ambiguity, structure, membership, unrouted Row, duplicate Row, synonymous Column,
and stale description findings are review-only; never infer the correct semantic placement from a scan.
SELECT duplicate Rows before proposing MERGE, inspect synonymous fields before a
Schema plan, and request review before Router splits or description rewrites.

Semantic-health findings are review-only in v2. Do not submit a maintenance
mutation for them automatically. Use the normal Schema, Router, or Row mutation
flow after the AI has inspected the affected logical objects and the user has
approved any broad or destructive change.

```sh
memora maintain --report
```

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

## Record feedback and revise

Record useful, irrelevant, stale, wrong, or incomplete quality feedback against
the exact displayed Database, Table, Row ID, and revision. A feedback event is
an auditable quality signal only: it never runs MSQL or changes facts, History,
indexes, or Route memberships.

```sh
memora feedback --event '{"version":"memora.feedback-event/v1","event_id":"feedback-10","kind":"wrong","actor":"agent:host","reason":"user says the summary is wrong","target":{"database":"work","table":"notes","row_id":"row_01","revision":2}}'
```

For stale, wrong, or incomplete feedback, re-SELECT the current Row and wait for
an explicit user confirmation with a new source event. Submit either a normal
revision Mutation Plan or an undo request in `memora.feedback-confirmation/v1`.
Keep scope, actor, provenance, expected revision, and the feedback ID bound to
the confirmation. Never mutate useful/irrelevant feedback or expand its scope.

Logical undo uses RESTORE and appends a new `COMPENSATE` revision. It never
deletes History or rewinds the current revision. Supply the expected schema and
current revisions plus complete index and Route snapshots. If a confirmation is
in doubt, inspect logical Row History before recovery; never blindly replay it.
Only a verified `memora.feedback-confirmation-receipt/v1` establishes success.

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
