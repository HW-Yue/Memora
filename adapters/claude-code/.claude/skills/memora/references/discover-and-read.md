# memora — Discover a Database and read facts

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

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
user has named a Database; never widen or invent the scope. `work`, `notes`,
`row_01` and `route_*` in the examples below are **placeholders** — substitute
your own Database, Table, Row and Route names; `actor` is free text naming who
is acting (`agent:host` here), not a fixed literal.

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
Database, continue the bounded discovery below with that exact name. **When
there is no human in the loop** — a subagent, a scheduled run, a host that
cannot ask — do not stall and do not guess silently: print the discovered names
with their purposes as your receipt, bind the one Database whose declared
`purpose`/`scope` covers the question, and **state that inference in your
answer**. If two or more Databases could cover it, stop and report the
candidates instead of choosing. Never widen the scope to make a guess fit.

```sh
memora query --input '{"parameters":{"named":{"limit":64,"bytes":8192}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW CATALOG ATLAS LIMIT :limit BYTES :bytes COMPACT"
memora query --input '{"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "DESCRIBE TABLE work.notes COMPACT"
memora query --input '{"parameters":{"named":{"cursor":"","limit":12}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTES FROM TABLE work.notes AT ROOT CURSOR :cursor LIMIT :limit"
```

The Atlas already carries every Table of every Database it returns — name,
`purpose` and `scope` — so `SHOW TABLES FROM <db>` adds nothing unless the Atlas
page came back `truncated` and you need one Database's Table list on its own.
`SHOW DATABASES` itself returns `tables: []` on every row — it never names a
Table, so the Atlas call is mandatory before you can choose one: it names Databases
only, which is why the Atlas is the step that follows it.
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
Keyword recall (`RECALL … MATCH`) and vector recall (`RECALL … NEAREST`) are the
other two product paths; jev is the fourth and only optional one, a Skill-side
chooser that reads no database state of its own.

Treat Route results as `navigation_only`. They are neither answers nor evidence.
Explicitly choose one or more Tables from the compact Atlas. For a selected
Table, issue the ordinary Router root and continue the normal layer-by-layer
state machine.

Answer only from revision-matched SELECT rows after normal Route navigation and
RowID lookup.

**Completeness.** Enumerate every Table of the Database you bound; a Table is out
of scope only when its declared `purpose`/`scope` excludes the question —
"it looked unrelated" is not a reason. Census the Tables that could hold an
answer, and say in your answer what you read and what you did not. An unread
Table you never mention is an answer that looks complete and is not.

**A census is a plain SELECT with no `WHERE`.** `SELECT row_id, title, revision
FROM <table> LIMIT :limit` is legal, counted against `select_rows` exactly like a
point read, and it is the cheapest way to see everything a Table holds: it
returns each Row's `row_id` **and** its `route_paths`, so it locates the Rows
without walking the tree. Census every Table of the bound Database in **one
request** — one statement per Table, one `--input` element per statement — then
point-read the Rows that matter in a second request: that is the shortest honest
path to a complete, cited answer, and on Tables that fit it replaces the tree walk
entirely. When a question needs more than one Row, that census —
followed by point reads of the Rows that matter — replaces the whole
`SHOW ROUTES … → OPEN ROUTE → SELECT` chain, and no Route walk is needed first.
Walk the tree when you are looking for *where something is*, or when the Table is
larger than `select_rows` (see the caveat below).

A census is complete when it returned **every** Row, and the flag that says so is
`truncated`: `true` when the scan budget or your own `LIMIT` stopped the listing
short, `false` when the answer is everything. Trust it over arithmetic. Leaf count
and Row count agreeing is *route-mount integrity* (`orphan_rows`,
`multi_leaf_rows`, `mismatched_mounts`), not a statement about what a SELECT
returned — a census is complete even on an instance where those counters are
non-zero.

**A census that comes back `truncated: true` cannot be continued with another
`SELECT`.** That statement has no cursor, its `WHERE` takes only `row_id`
equality, and the budget can only be raised by a write (`ALTER CONFIGURATION`,
below). The read-only continuation is a different surface: above `select_rows`
the sanctioned enumerator is the **Route tree walk**: `SHOW ROUTES` pages with a
cursor at `route_children` per level, and every leaf names its Row. You do not
have to remember that: the truncated answer carries an `output_truncated` warning
whose `details.enumerate_with` is the statement to run. Take a Table's row count
from the census itself, not from `doctor`, whose `rows` is instance-wide.
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

The `WHERE` surface is deliberately narrow: **one equality on `row_id`**, joined
by `AND` when you need more than one condition. `IN (…)`, `OR` and `JOIN` are not
part of it — and omitting `WHERE` entirely is not an error, it is the census
above. **Read one Row per statement.** To read several Rows, send several
statements in one request — `--input` takes one object per statement as an array,
in source order, for `query` and `exec` alike, and **each element binds its own
`parameters.named`** (the element at the same index as its statement), so two
statements can read two different Rows:

```sh
memora query --input '[{"parameters":{"named":{"row":"row_01"}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}},{"parameters":{"named":{"row":"row_02"}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}]' "SELECT title, summary, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1; SELECT title, summary, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1"
```

Those two elements bind `row_01` and `row_02` respectively — the same parameter
name, a different value per statement. Names may also differ between statements
(`:first` in one, `:second` in the next), because each element's `parameters.named`
is read for that statement alone.

`links` and `route_paths` ride along on every returned Row whether or not you
projected them, and `columns` lists only the fields you asked for — do not try to
project the attached ones. A TEXT column's declared ceiling travels with it as
`max_characters`, counted in Unicode code points: `summary` is a ~1,000-character
document inside a `TEXT(2500)` ceiling, and a reader can check that without
guessing at bytes. A **point read** (one that names a `row_id`) also returns a
`row_detail` object — **once per result, beside `rows`, not per Row**, and absent
(`omitempty`) on a census: schema version, `row_semantics`, the display map naming
the title and summary columns, and `created_at`/`updated_at`, which is the only
way to say whether a Row that describes itself as a living log has actually been
written since (`revision` stays 1 until someone writes, so it cannot date a
"current status"). `DESCRIBE TABLE` is where that shape comes from if you need it
before reading. So a point-read batch costs roughly 1.5–2 KB per Row beyond the
facts, while a census pays for `columns` only.

**Date your evidence when the answer is about "now".** A Row's `row_detail.updated_at`
says when that revision was written; `SHOW HISTORY FROM <table> FOR ROW :row LIMIT :limit`
lists the revisions of that one Row; and the commit log —
`SHOW CHANGES IN DATABASE :database LIMIT :limit` — is the audit surface that dates
a whole tree (`IN DATABASE` is required, and change entries carry no column values,
only Row IDs and revisions). `doctor`'s `changes` and `rows` are instance-wide
counters: use them to notice that a tree is frozen, never as a substitute for
reading it. `doctor`'s `route_nodes` also counts the Table **roots**, which no
`SHOW ROUTES` page ever returns, so a full walk will always come up short by one
node per Table — read each root's `route_id` from the `parent_id` of its children
instead of trying to reconcile the total by counting.

There are two ways from a Table to its facts, and the question picks one:

- **Census** (enumerating: "what have I done", "what is in here") —
  `SELECT row_id, title, revision FROM <table> LIMIT :limit`, then point reads of
  the Rows that matter. `route_paths` comes back with every Row, so nothing else
  is needed.
- **Navigation** (locating: "where is the thing about X", or a Table too large to
  census) — walk the tree, then read the Row the leaf points at.

`SHOW ROUTES … AT ROOT` returns the root's **children**, not the root node itself
(the children carry the root's `route_id` as their `parent_id`, and no row
describes the root). Because of that, one tree has three path spellings in three
surfaces, and a host that wants to compare them normalises deliberately:
`route_paths` on a Row is root-**less** (`["/技法/红烧"]`), a recall hit's `path` is
segments **with** a literal `root` segment (`root → 技法 → 红烧`), and an archive
path is a single string that names it (`"/root/技法/清蒸"`).

```text
census:     SHOW CATALOG ATLAS → DESCRIBE TABLE → SELECT row_id, title, revision LIMIT n
            → SELECT the Rows that matter → answer only from revision-matched rows

navigation: SHOW CATALOG ATLAS → DESCRIBE TABLE
            → SHOW ROUTES FROM TABLE ... AT ROOT
            → choose one node → SHOW ROUTES UNDER ... (repeat as needed)
            → OPEN ROUTE on a leaf → validate database/table/Row/revision locators
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

The five budgets are counted **per statement**, not per request: a batch of ten
point reads is ten statements of one Row each, and each one is measured on its
own. Read them only when a limit actually binds or you intend to exceed the
bundled ceilings, with the statement that returns them — `SHOW CONFIGURATION`
(the five `query_budgets` are `route_children`, `open_locators`, `select_scan`,
`select_rows`, `route_frame_nodes`; `SHOW CONFIGURATION HISTORY` shows how they
got there). The bundled ceilings are `route_children` 12, `open_locators` 1,
`select_rows` 10, `select_scan` 1000 and `route_frame_nodes` 12,000 context
characters. `select_scan` is the one easy to forget and the one that cuts a
census without the `LIMIT` looking wrong: it caps how many candidate rows one
SELECT examines before it reports `truncated`. `open_locators` is retained as a
compatibility budget but cannot raise a leaf above its `0..1` cardinality. All
five are counted **per statement**: a batch of ten point reads is ten statements
of one Row each, so it never approaches `select_rows`.

`select_rows` is a **hard failure, not a clamp**: `SELECT … LIMIT 50` is refused
rather than truncated, which is why the census uses a `LIMIT` you know fits. Read
`SHOW CONFIGURATION` when a `LIMIT` is refused or when you expect a Table to
exceed the ceiling — not as a ritual before every read. A Table that genuinely
holds more live Rows than the ceiling is enumerated read-only by the Route tree
walk, or the ceiling is raised explicitly:

```sql
ALTER CONFIGURATION QUERY_BUDGETS SET
  ROUTE_CHILDREN :routes, OPEN_LOCATORS :locators, SELECT_SCAN :scan,
  SELECT_ROWS :rows, ROUTE_FRAME_NODES :frame;
```

It replaces all five (they are one revision, with `expected_revision`, actor and
reason), `SHOW CONFIGURATION HISTORY LIMIT :limit` shows the trail, and
`RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION :revision` appends a compensating
revision. Raising a budget is a deliberate act with a reason, not a reflex. A locator cursor is never
expected from a valid leaf. Drop the Route Frame when its schema or route
revision is stale, the topic changes, or the task ends.

Stop when enough SELECT evidence answers the question, all candidates are
exhausted, a hard budget is reached, access is denied, or another call cannot
change the answer. Cite `database.table`, Row ID, revision, and available source
anchor for every factual summary. Distinguish “no matching Row,” “truncated,”
“stale during SELECT,” and “permission denied.”
