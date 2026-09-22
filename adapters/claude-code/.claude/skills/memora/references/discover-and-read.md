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
memora query --input '{"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTES FROM TABLE work.notes AT ROOT"
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
Keyword recall (`RECALL … MATCH`) answers when you cannot name the node; the fused
two-arm recall (`MATCH … NEAREST`, one statement) needs a query vector, which
`scripts/embed_query.py` produces — with no provider configured it exits `2` and
the recall is keyword-only, which the answer has to say rather than call it
fused. jev is the fourth and only optional one, a Skill-side chooser that reads no
database state of its own — and it is a **fallback**, not a front door: it answers
one layer at a time and it never produces the answer.

### Which jev question a layer needs

The shape follows the **layer**, not a guess about the user's intent:

- children that are **alternatives**, only one of which can be right ("which
  Table holds this?") → `"mode":"choice"`, one `Choice`, below;
- children that are **instances of one kind**, several of which may apply ("which
  of my internships?") → `"mode":"set"`, one `Noul` per child. A `Choice` here
  can only ever return one, and asking it for two is how the second internship
  goes missing. Measured on a real library with two internships: the set answer
  returned both, and with a single-target intent it returned one — same request
  shape, same ~0.9 s.

### When jev is worth a call

Use it only when all three of these hold, and decide the layer yourself in every
other case:

- the layer has **at least two live children** — the script refuses fewer
  (`exit 4`), and with one child there is nothing to choose;
- **the question you hand it is single-target** ("which of these places holds
  this?"). The *task* may want everything and still use jev here — the
  enumeration happens afterwards with `SELECT`, and a layer whose children are
  instances of one kind is a `set` question, not a `choice` one. What must not
  happen is asking a `Choice` to enumerate: it always returns one, so a
  two-internship layer asked for both comes back with one of them — or, with the
  `none` option and no `all`, with `none` at 0.97, which is the honest refusal but
  still not the answer;
- **in `choice` mode**, you put a `none` option in the option set (below). This one
  is not optional bookkeeping: `Choice` always picks something, so an unanswerable
  question comes back as a **confident wrong answer**. `set` mode needs no `none`
  option — it asks a floor probe of its own for every request.

The contract, exactly — options carry name and purpose, **never** a `route_id`:

```sh
# choice: one child, for a layer of alternatives. Keep the `none` option.
echo '{"intent":"<what the user wants>","options":[{"name":"<child>","purpose":"<its purpose>"},{"name":"none","purpose":"none of these places is where this intent lives"}]}' \
  | python3 "<skill-directory>/scripts/jev_select.py"
# -> {"mode":"choice","choice":"<name>","confidence":0.0-1.0,"probabilities":{…},"model":"…","elapsed_ms":0}

# set: several children may apply. No `none` option — the script adds its own.
echo '{"mode":"set","intent":"<what the user wants>","options":[{"name":"<child>","purpose":"<its purpose>"},{"name":"<another>","purpose":"<its purpose>"}]}' \
  | python3 "<skill-directory>/scripts/jev_select.py"
# -> {"mode":"set","relevant":["<name>",…],"decision":"separated|undecided|empty","model":"…","elapsed_ms":0}
```

**In `set` mode no probability comes back.** The cut happens inside the script, so
what you receive is a set of names and one of three decisions; there is nothing to
threshold, rank or store. The rule is the same one every time, and it is relative
to the request rather than calibrated: alongside the per-child questions the
script asks a **floor probe** ("is it true that none of these places holds any part
of the intent?"), ranks the answers and the probe together, and cuts at the
largest gap — provided that gap is at least a factor of two, which is a fixed
constant and deliberately not configurable, because it measures whether the model
answered decisively this time rather than how relevant anything is.

Act on the decision:

- `separated` → those children are the set. Descend into each, and answer from the
  Rows they locate;
- `undecided` → the model answered but did not separate them: **enumerate the
  layer** (`SHOW ROUTES` and read it) instead of trusting a filter it did not
  make;
- `empty` → nothing here answers the intent, the same signal as `none`: go up a
  layer, or switch to `RECALL`.

And the case this rule exists beside: if the user plainly wants everything and the
layer's children are all of it, do not call jev at all — enumerate. jev is for
layers that mix what is wanted with what is not.

`model` is the model that **served** the answer (`jev-1.13.0` in practice, while
the default asked for is the `jev-latest` alias) — report what came back, not the
alias. `elapsed_ms` is the provider round trip, so the budget below is something
you can check instead of assume. `--dry-run` prints the request that would be
sent and sends nothing (no key needed) — that is how to confirm the shape, and
that no `route_id` travelled. `--min-confidence FLOAT` refuses a weak answer as
`exit 5` instead of returning it, with the choice and `elapsed_ms` still on
stdout. Both flags are in `python3 "<skill-directory>/scripts/jev_select.py" --help`.

Exit codes: `0` answered, `2` no `TYPESAFE_API_KEY`, `3` the provider refused,
`4` it was not a choice (fewer than two options, empty intent), `5` below
`--min-confidence`. Map the chosen **name** back to the `route_id` you already
hold from `SHOW ROUTES`, then send the next `SHOW ROUTES UNDER`.

Act on `none`: go back up a layer, or switch to `RECALL`. A low confidence is not
a substitute for it — confidence measures how concentrated the distribution was,
not whether the answer is right, and a 0.9 can be the wrong pick.

**Budget it honestly.** Each decision is a fresh process and a fresh TLS
connection to a hosted API — one measured library spent **0.9–1.0 s per decision**
on repeat calls and 2.0 s on a cold first one, of which the model itself is
~0.2 s; the option count barely moves it (12 options ≈ 2 options, +0.1 s) and the
layers are decided serially. On a query the cheaper paths already answer, jev
earns nothing: one real run of "list my internships" used it at exactly one of
four layers (the Table choice) and got the rest from the census. One obvious
child, or a question `RECALL` answers, is not worth a second.

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
the sanctioned enumerator is the **Route tree walk**: one `SHOW ROUTES` per level
returns that whole level — a layer is bounded by `route_policy.branch_fanout`, so
there is no page and no cursor — and every leaf names its Row. You do not
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

**A leaf id from earlier still works after the tree moved.** A `SPLIT` or `MERGE`
retires the nodes it replaced but keeps them as redirects: reading such a leaf,
by `OPEN ROUTE` or as a Row's `route_paths`, lands on its successor instead of
failing. So an old id resolving to a Row that now sits elsewhere is the designed
continuity, not a broken mount — the retired node is not a live leaf and no
longer counts as one (`doctor`'s `route_nodes` counts live nodes only). Answer
from the path and revision you got back, and re-read the tree if the *position*
is what the question is about.

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
`SHOW CHANGES IN DATABASE work LIMIT :limit` — is the audit surface that dates
a whole tree (`IN DATABASE` is required, it takes the Database **name as an
identifier** so a parameter there does not parse, and change entries carry no
column values, only Row IDs and revisions). `doctor`'s `changes` and `rows` are instance-wide
counters: use them to notice that a tree is frozen, never as a substitute for
reading it. `doctor`'s `route_nodes` also counts the Table **roots**, which no
`SHOW ROUTES` answer ever returns, so a full walk will always come up short by one
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
memora query --input '{"parameters":{"named":{"parent":"route_architecture"}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTES UNDER :parent"
memora query --input '{"parameters":{"named":{"leaf":"route_storage","limit":1}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "OPEN ROUTE :leaf LIMIT :limit"
memora query --input '{"parameters":{"named":{"row":"row_01","limit":10}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SELECT title, summary, row_id, revision FROM work.notes WHERE row_id = :row LIMIT :limit"
```

The five budgets are counted **per statement**, not per request: a batch of ten
point reads is ten statements of one Row each, and each one is measured on its
own. Read them only when a limit actually binds or you intend to exceed the
bundled ceilings, with the statement that returns them — `SHOW CONFIGURATION`
(the four `query_budgets` are `open_locators`, `select_scan`, `select_rows` and
`route_frame_nodes` — `route_children` was the `SHOW ROUTES` page budget and went
with route paging; `SHOW CONFIGURATION HISTORY LIMIT :limit` shows how they got
there). The bundled ceilings are `open_locators` 1,
`select_rows` 10, `select_scan` 1000 and `route_frame_nodes` 12 **nodes** — that
last one is the host's own bound on a cross-statement Route Frame: the engine
stores and returns the number, and enforcing it is the host's job, so exceeding
it is not an engine error. It is accepted in `1..100`, and the budget statement
below has to carry every one of the five. `select_scan` is the one easy to forget and the one that cuts a
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

```sh
memora exec --input '{"parameters":{"named":{"locators":1,"scan":1000,"rows":10,"frame":12}},"mutation":{"expected_revision":1,"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-7","reason":"raise the census ceiling for one large Table"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L2"}}' "ALTER CONFIGURATION QUERY_BUDGETS SET OPEN_LOCATORS :locators, SELECT_SCAN :scan, SELECT_ROWS :rows, ROUTE_FRAME_NODES :frame"
```

Changing configuration is an **L2** write, and the statement is only half of it.
It replaces all four (they are one revision, so the mutation needs
`expected_revision` — read it from `SHOW CONFIGURATION` first — plus actor and
reason, and `default_level` is `L2`; an L1 authorization is refused before the
revision is even looked at), `SHOW CONFIGURATION HISTORY LIMIT :limit` shows the trail, and
`RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION :revision` appends a compensating
revision (same input: `expected_revision`, actor, reason; the statement names the
revision to restore **to**). Raising a budget is a deliberate act with a reason, not a reflex. A locator cursor is never
expected from a valid leaf. Drop the Route Frame when its schema or route
revision is stale, the topic changes, or the task ends.

Stop when enough SELECT evidence answers the question, all candidates are
exhausted, a hard budget is reached, access is denied, or another call cannot
change the answer. Cite `database.table`, Row ID, revision, and available source
anchor for every factual summary. Distinguish “no matching Row,” “truncated,”
“stale during SELECT,” and “permission denied.”
