# memora — Write, revise, split and merge Rows

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

## Decide where knowledge lives

Before persisting a new piece of knowledge, decide where it belongs. Decide
from large to small scope and only create when reuse is impossible:

1. Reuse an existing Database whose purpose/scope clearly covers the user's
   topic and whose anti_scope does not exclude it.
2. Create a new Database only for a genuinely new domain — the user's first
   mention of a personal topic with no matching Database warrants a new
   Database, written before anything else, with an explicit purpose and scope.
3. Inside the chosen Database, reuse an existing Table whose purpose fits the
   knowledge; add a Row there.
4. Create a new Table only when no existing Table fits and the content is a
   distinct, recurring kind the user will keep adding to.
5. Never create on a hunch or from a name alone: match by the object's declared
   purpose/scope, not by guessing equivalence.
6. **The Columns are not yours to design.** Every Table has the same two
   Columns, and you copy the template below verbatim. Do not invent Columns, do
   not add a field because a value looks structured, and do not widen a Column
   except to hold a longer document (see "Evolve schemas"). Classification,
   status, dates, names and relationships belong in the Route tree, in the
   `summary` prose, or in `links` — never in a new Column. The engine enforces
   this: a Column that declares anything other than `ROLE title` or
   `ROLE summary` is refused, with the shape quoted back at you, on `CREATE
   TABLE`, on `ADD COLUMN` and in a Schema-change plan.

### The one Table shape

```sh
memora schema --plan '{
  "version": "memora.schema-plan/v1",
  "id": "schema-plan-1",
  "actor": "agent:host",
  "source_event_id": "conversation:event-1",
  "reason": "create the <table> Table with the canonical shape",
  "authorized_databases": ["<database>"],
  "ensure": {
    "database": {"name": "<database>", "purpose": "<what this Database holds>",
                 "scope": "<what belongs in it>", "anti_scope": "<what does not>"},
    "table": {
      "name": "<table>",
      "purpose": "<what this Table holds, as a topic>",
      "row_semantics": "一行是一份完整、可独立修改的语义文档",
      "columns": [
        {"name": "title", "type": "TEXT(200)", "nullable": false,
         "purpose": "文档标题", "semantic_role": "title"},
        {"name": "summary", "type": "TEXT(2500)", "nullable": false,
         "purpose": "完整自足的文档正文", "semantic_role": "summary"}
      ]
    }
  }
}'
```

- Copy the two `columns` objects byte for byte: the names, types, purposes,
  nullability and roles are fixed. Only the Database and Table names and their
  purpose/scope text change per Table.
- `purpose` is the sentence a later write reads to decide where knowledge
  belongs, so write it as "是什么", not as a row definition: `实习与工作经历`,
  not `一行是一段实习或工作经历`.
- `row_semantics` is the engine's own statement of what a Row is; keep the
  constant above. Reuse an existing Table whenever its `purpose` fits — never
  create a second Table of the same kind for a slightly different shape.
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

Every INSERT MUST write `title` and `summary`, because both columns are NOT NULL
and the engine refuses the write otherwise. An UPDATE may set only the fields it
changes — it is a partial write, and the engine keeps the columns it was not
given — but the Row must still read as a complete document afterwards, which is
what makes `title` and `summary` the shape rather than two more fields. `summary` is the Row's body: a complete,
self-contained Markdown document of roughly 1,000 CJK characters that a reader
can understand without opening anything else. It is not a one-line abstract,
not a bullet list, and not a restatement of `title`. A Row without a usable
`summary` is not a usable memory — never write one and never leave `summary`
empty to "fill in later". If the configured TEXT ceiling cannot hold the
document, submit a Schema change to widen the Column first (see
"Evolve schemas"); never silently truncate.

**Legacy Tables already carry Columns you did not choose** (a Database written
before this rule can have `company`, `role`, `period`, `highlights` and the
like). Keep writing them out of the picture: put the facts in `summary` and in
the Route tree, supply no value for the extra Columns, and never add another.
Do not read them to decide anything, and do not try to drop them on your own —
retiring them is a reviewed Schema change the user has to approve (see
"Evolve schemas").

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
memora exec --input '{"parameters":{"named":{"title":"Use SQLite","summary":"<the complete ~1,000-CJK-character Markdown document; abbreviated here>"}},"mutation":{"expected_schema_version":1,"max_affected_rows":1,"route_path":[{"name":"architecture","kind":"branch","purpose":"Architecture decisions"},{"name":"sqlite","kind":"leaf","purpose":"Why SQLite"}],"actor":"agent:host","source":"conversation:event-7","reason":"record the decision"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "INSERT INTO work.notes (title, summary) VALUES (:title, :summary)"
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
## Return a receipt

After a mutation, return a receipt under 2,000 characters with the logical
objects changed, action, revision/commit sequence, reason/source, verification
result, warnings, truncation, and any required follow-up. After a read, cite the
database/table/Row IDs used and distinguish missing data from denied or truncated
data. Never claim success from an error envelope or incomplete source coverage.
