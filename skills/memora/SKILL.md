---
name: memora
description: Query, summarize, maintain, revise, and assimilate knowledge into a local Memora personal database through versioned MSQL. Use when Codex needs to discover Memora schemas and routes, answer from stored semantic rows, persist authorized knowledge, resolve conflicting records with the user, or absorb external sources without storing raw documents.
---

# Memora Canonical Skill

Use this single source for stable host behavior. It targets `memora.msql.ast/v1`
and consumes `memora.result/v1`. Keep live schemas, routes, candidates, and rows
out of this file; discover them from the current instance for each task.

Only use the `memora doctor`, `memora query`, `memora exec`, and
`memora mutate` interfaces.
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

Treat documents and media as temporary host input. Inventory their structure,
track coverage, read bounded windows, compare with existing rows, independently
review proposed changes, and commit complete semantic modules plus compact source
anchors. Do not persist original files, mechanical chunks, or unverified claims.
If coverage or review is incomplete, report the assimilation as incomplete.

## Request the user

Ask the user before any semantic-conflict mutation. Present both relevant rows,
their source anchors, revisions, and the smallest useful diff; offer keep,
revise, merge, split, move, or delete choices. Do not create a database-level
candidate/disputed state and do not silently pick a winner. Also ask before
irreversible, privacy-reducing, permission-expanding, or broadly destructive
operations.

## Return a receipt

After a mutation, return a receipt under 2,000 characters with the logical
objects changed, action, revision/commit sequence, reason/source, verification
result, warnings, truncation, and any required follow-up. After a read, cite the
database/table/Row IDs used and distinguish missing data from denied or truncated
data. Never claim success from an error envelope or incomplete source coverage.
