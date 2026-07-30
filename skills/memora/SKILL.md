---
name: memora
description: Query, summarize, maintain, revise, and assimilate knowledge into a local Memora personal database through versioned MSQL. Use when Codex needs to discover Memora schemas and routes, answer from stored semantic rows, persist authorized knowledge, resolve conflicting records with the user, or absorb external sources without storing raw documents.
---

# Memora Canonical Skill

Use this single source for stable host behavior. It targets `memora.msql.ast/v1`
and consumes `memora.result/v1`. Keep live schemas, routes, candidates, and rows
out of this file; discover them from the current instance for each task.

Only use the `memora assimilate`, `memora doctor`, `memora query`, `memora exec`,
`memora feedback`, `memora maintain`, `memora mutate`, `memora schema`, and
`memora reflect` interfaces.
Never inspect, edit, copy, or infer state from physical database, index, journal,
page, or instance files. Logical MSQL results are the only source of database
truth available to the host.

## Discover

Start a new task or stale Route Frame with bounded discovery. Inspect databases,
then the selected schema and Router. Reuse an existing semantic scope when it
fits; do not invent a table from a name alone.

```sh
memora doctor
memora query "SHOW DATABASES"
memora query "DESCRIBE TABLE work.notes COMPACT"
memora query --input '{"parameters":{"named":{"database":"work","path":"/","cursor":"","limit":12}}}' "SHOW ROUTES FROM DATABASE :database AT :path CURSOR :cursor LIMIT :limit"
```

## Query and summarize

Expand the user's intent into a short, deduplicated `terms` list. Route or MATCH
may return only locators; never answer from those candidates. Select projected
semantic fields by Row ID, then summarize only the returned rows. Report empty,
truncated, stale, or permission-limited results instead of filling gaps.

Use this bounded state machine:

```text
SHOW DATABASES → DESCRIBE TABLE
→ optional OPEN ROUTE (empty/not found falls back)
→ MATCH
→ validate database/table/Row/revision locators
→ SELECT projected fields + row_id + revision
→ answer only from revision-matched SELECT rows
```

Generate 1–32 non-empty, case-insensitively deduplicated query terms. Use an
existing Route only when it plausibly scopes the question. Do not broaden a
permission denial. If a selected Row changed, discard it and refresh discovery
at most once when it can materially affect the answer.

```sh
memora query --input '{"parameters":{"named":{"query":"routing design","terms":["routing","router","路由"],"limit":24}}}' "MATCH work.notes QUERY :query TERMS :terms LIMIT :limit"
memora query --input '{"parameters":{"named":{"row":"row_01","limit":10}}}' "SELECT title, summary, row_id, revision FROM work.notes WHERE row_id = :row LIMIT :limit"
```

Keep at most 12 Router rows, 24 candidate locators, 10 selected rows, and 12,000
characters of combined working context. Follow cursors only while another page
can materially change the answer. Drop the Route Frame when its schema or route
revision is stale, the topic changes, or the task ends.

Stop when enough SELECT evidence answers the question, all candidates are
exhausted, a hard budget is reached, access is denied, or another call cannot
change the answer. Cite `database.table`, Row ID, revision, and available source
anchor for every factual summary. Distinguish “no matching Row,” “truncated,”
“stale during SELECT,” and “permission denied.”

## Write

Within the user's authorized scope, use:

```text
Discover → query existing rows → plan → validate → execute → verify
```

Choose IGNORE, INSERT, REVISE, MERGE, SPLIT, MOVE, or RELATE before generating
MSQL. Prefer revising an existing semantic module over appending a duplicate.
Use parameters, expected schema/revision, a maximum affected-row count, actor,
source, reason, complete semantic index terms, and current route memberships.
Keep transactions short and verify the returned revision and logical row.

Build one `memora.mutation-plan/v1` object. Every decision includes at least one
read-only preflight with explicit Row expectations. IGNORE has no steps. INSERT,
REVISE, MOVE, and RELATE have one step; MERGE is one UPDATE plus DELETE steps;
SPLIT is one UPDATE plus INSERT steps. Keep at most eight steps. Every INSERT or
UPDATE supplies the complete deduplicated `index_terms` and `route_leaf_ids`
snapshots, including explicit empty arrays. Submit the plan through `mutate` so
Policy validation occurs before any Tool call and multi-step changes share one
short transaction.

```sh
memora exec --input '{"parameters":{"named":{"row":"row_01","summary":"Route results are locators only"}},"mutation":{"expected_schema_version":1,"expected_revision":2,"max_affected_rows":1,"index_terms":["routing","locator"],"route_leaf_ids":["route_query"],"actor":"agent:host","source":"conversation:event-7","reason":"refine verified conclusion"}}' "UPDATE work.notes SET summary = :summary WHERE row_id = :row"
memora mutate --plan '{"version":"memora.mutation-plan/v1","id":"plan-7","decision":"IGNORE","database":"work","table":"notes","actor":"agent:host","source_event_id":"conversation:event-7","reason":"existing Row already captures it","authorized_databases":["work"],"preflight":[{"id":"duplicate-check","msql":"SELECT row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1","input":{"parameters":{"named":{"row":"row_01"}}},"expect_rows":1}],"steps":[],"verify":[]}'
```

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

Use a migration plan for renames. Include the expected Database and object
schema versions and a hard maximum affected-object count. Schema v1 accepts only
reversible Table and Column renames. The command applies each autocommit DDL in
order and compensates completed renames in reverse order after a failure. Treat
`rolled_back` as a failed migration with a verified recovery receipt, not as a
successful schema change. Ask the user before an irreversible or broad change.

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
Treat `memora.semantic-health/v1` issues as deterministic candidates, not facts.
SELECT duplicate Rows before proposing MERGE, inspect synonymous fields before a
Schema plan, and request review before Router splits or description rewrites.

The only v1 auto-fix is `retry_reindex` on an issue explicitly marked
`low_risk` and `auto_fix=true`. Submit its issue ID with the exact report hash in
`memora.maintenance-request/v1`; stop on a revision conflict. Never place a
review-required issue in that request. Return the bounded
`memora.maintenance-receipt/v1` and do not claim that a retry already rebuilt
the index.

```sh
memora maintain --report
```

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

## Return a receipt

After a mutation, return a receipt under 2,000 characters with the logical
objects changed, action, revision/commit sequence, reason/source, verification
result, warnings, truncation, and any required follow-up. After a read, cite the
database/table/Row IDs used and distinguish missing data from denied or truncated
data. Never claim success from an error envelope or incomplete source coverage.
