# memora — Write, revise, split and merge Rows

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

## Decide where knowledge lives

Before persisting a new piece of knowledge, decide where it belongs. Decide
from large to small scope and only create when reuse is impossible:

1. Reuse an existing Database whose purpose/scope clearly covers the user's
   topic and whose anti_scope does not exclude it. **The choice is yours to
   make**: never ask the user which Database. Say which one you bound, and the
   declaration you matched, in your answer (see "Report the landing" below).
2. Create a new Database yourself when nothing covers it — a genuinely new
   domain is the user's first mention of a personal topic with no matching
   Database, so write the new Database before anything else, with an explicit
   purpose and scope. That is the last resort after looking, not the first
   guess: a new Database is justified only once the declared purposes of the
   existing ones have been read and none fits, because a discovery that came
   back empty because it was incomplete is how one memory shatters into several
   Databases.
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

**Report the landing.** Every write's answer names the place it wrote: the
Database and Table, the Route path (or the leaf id), the `row_id`, and the
`purpose`/`scope` sentence that put it there. When it created the Database, say
so explicitly and quote the purpose and scope written for it — you are the one
who invented that declaration, so it is a decision to show, never evidence to
lean on. The receipt is what makes the autonomy reviewable after the fact; it
never stands in for looking first.

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

**"记一下" / "save this" is a dedupe request before it is a write.** Search the
subject first — `RECALL`, a census, or the Route walk — because the honest answer
is often "already recorded". When an existing Row covers the subject, the correct
action is a REVISE of that Row (or an IGNORE plan carrying the reason); a second
Row for the same subject is how one memory starts contradicting itself.

**After writing a capability or status fact, sweep for its opposite.** Stale
gap-lists and "待做" notes are exactly where the contradiction hides: search the
words your new fact denies, and revise what still asserts them. This is a step of
the write, not a separate task.

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

Let the content decide how it is laid out: when a document has parts that are
different in kind, write them as visibly different layers — a heading, a short
list, a separate paragraph — instead of running everything into one block. One
wall of text reads as less than it is, whatever it contains. Nothing more
prescriptive than that belongs here.

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
UPDATE names exactly one Route leaf, in one of the two forms below, and the two
are mutually exclusive: a Row occupies exactly one Leaf, and a Leaf holds at most
one live Row. An empty array is not a mount, and a Row with no Route membership
can never be reached by semantic navigation. The plan carries whichever form the
statement does — `mutate` accepts both, so a planned write and a one-shot write
have the same shape.

### Create the Route leaf you are about to write into

Discovery statements (`SHOW ROUTES`, `OPEN ROUTE`) only navigate an existing
tree. Creating the semantic index itself uses `CREATE ROUTE`, which runs at
risk level **L2** — the L1 level used for Row writes is refused:

```text
CREATE ROUTE ROOT FOR TABLE <db>.<table> PURPOSE :purpose [SYNOPSIS :synopsis]
CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose [SYNOPSIS :synopsis]
```

`KIND` is `'branch'` for a grouping node and `'leaf'` for a node that locates a
Row. Both forms return the new `route_id`.

**`PURPOSE` has to describe the place, not repeat its name.** A purpose equal to
the name — after folding case, width and padding — is refused
(`validation_error`), because the semantic tree is read through exactly these two
fields: the layer-by-layer walk offers a candidate as `name` + `purpose` and
nothing else, so `NAME 'rekey' PURPOSE 'rekey'` is a node nobody, model or
owner, can place. Write the sentence the owner would use: *"换了 embedding 模型
或维度之后，怎么把整库向量安全换过去"*. The same rule applies to every segment a
`route_path` **creates** and to every `SPLIT`/`MERGE` target. It does not apply
to a segment that already exists: an old Route whose purpose still repeats its
name is reported, not refused, so a library can be written to while its purposes
are being filled in.

**Repairing one of those is `ALTER ROUTE :route SET PURPOSE :purpose`** — a
purpose is an amendable description, not identity frozen at creation, and the
statement is judged by the same rule: a purpose folding down to the Route's own
name is refused, so the amendment either writes a real description or fails.
It is an **L2** write and needs the `expected_revision` you just read, like every
other Route mutation. `memora doctor` counts the backlog in
`routes_without_purpose` and names them in `routes_without_purpose_paths`.

```sh
memora exec --input '{"parameters":{"named":{"route":"route_abc","purpose":"换了 embedding 模型或维度之后，怎么把整库向量安全换过去"}},"mutation":{"expected_revision":3,"expected_schema_version":1,"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-9","reason":"the label said nothing"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L2"}}' "ALTER ROUTE :route SET PURPOSE :purpose"
```

A Row write mounts on exactly one
leaf, and it says so one of two ways — never at the top level of the request,
always inside `mutation`:

- `route_path`: name the path and let the engine reuse or create the segments.
  INSERT only, and never together with `route_leaf_ids`. This is the one the
  INSERT example below uses, and the one to prefer when the leaf may not exist
  yet: it stays the same L1 write in the same transaction, so naming a new
  position costs no extra statement and cannot leave a half-built path behind.
  A delete removes the Row's leaf and prunes whatever branch the removal left
  empty, which is why creating your own position is part of writing the Row
  rather than a structural change.
- `route_leaf_ids`: hand over the `route_id` you already hold — the `UPDATE`
  example does, and so does an INSERT into a leaf you created with `CREATE ROUTE`.

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
memora query --input '{"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTES FROM TABLE work.notes AT ROOT"

# 2. Create the root (L2, parameterised).
memora exec --input '{"parameters":{"named":{"purpose":"Semantic navigation root for work notes"}},"mutation":{"expected_schema_version":1,"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-7","reason":"bootstrap router"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L2"}}' "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose"

# 3. Create a branch, then a leaf under it, reusing the returned route_id.
memora exec --input '{"parameters":{"named":{"parent":"route_root","name":"architecture","kind":"branch","purpose":"Architecture decisions"}},"mutation":{"expected_schema_version":1,"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-7","reason":"group architecture notes"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L2"}}' "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose"

# 4. Verify: the child appears under the parent, and the leaf is still empty.
memora query --input '{"parameters":{"named":{"parent":"route_root"}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ROUTES UNDER :parent"
memora query --input '{"parameters":{"named":{"leaf":"route_leaf"}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "OPEN ROUTE :leaf LIMIT 1"
```

This bootstrap is ordinary Router construction, not a Route mutation plan.
`PLAN ROUTE MUTATION` only restructures an existing tree (SPLIT, MERGE, MOVE);
it cannot create the first root, and it is not the path for adding a leaf to
hold a new Row.

### Or name the path and let the kernel complete it

**This one needs the Table's root to exist already.** `route_path` completes a
path under an existing root; on a Table with no root the INSERT is refused
(`table "…" has no route root yet: create it explicitly, its purpose is
table-level semantics`). Create the root with `CREATE ROUTE ROOT` first (next
section), then come back.

An INSERT may carry `route_path` instead of `route_leaf_ids`: one entry per
segment, each with its own `name`, `kind` and `purpose` — and a purpose that
describes the segment rather than repeating its name, for the segments this
write creates (see above). The kernel reuses the
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
leaf and cannot also be reached through a second one.

**Send the write through `memora mutate` when you can.** A plan carries the same
`input` object per step — mount form included, `route_path` or `route_leaf_ids` —
plus read-only preflight and verify checks, so Policy validates it before any tool
call and a multi-step change shares one short transaction. `exec` stays right for
a one-off read or a statement you have already planned; it is not the way around a
plan. `expect_rows` is your own claim about what the check must find, so set it to
what the search above must return — 0 when the subject is genuinely new:

```sh
memora mutate --plan '{"version":"memora.mutation-plan/v1","id":"plan-8","decision":"INSERT","database":"work","table":"notes","actor":"agent:host","source_event_id":"conversation:event-7","reason":"record the decision","authorized_databases":["work"],"preflight":[{"id":"dedupe","msql":"SELECT row_id, revision FROM work.notes LIMIT 10","expect_rows":0}],"steps":[{"id":"insert","kind":"INSERT","target":"work.notes","msql":"INSERT INTO work.notes (title, summary) VALUES (:title, :summary)","input":{"parameters":{"named":{"title":"Use SQLite","summary":"<the complete ~1,000-CJK-character Markdown document; abbreviated here>"}},"mutation":{"expected_schema_version":1,"max_affected_rows":1,"route_path":[{"name":"architecture","kind":"branch","purpose":"Architecture decisions"},{"name":"sqlite","kind":"leaf","purpose":"Why SQLite"}],"actor":"agent:host","source":"conversation:event-7","reason":"record the decision"}}}],"verify":[{"id":"read-back","msql":"SELECT row_id, revision FROM work.notes LIMIT 10","expect_rows":1}]}'
```

A REVISE plan has the same shape with `"decision":"REVISE"`, an UPDATE step, and
`expected_revision` next to the `route_leaf_ids` snapshot. Two things about the
command itself, because both invite a wrong guess: `memora mutate` prints the
**receipt object alone** on stdout — `memora.mutation-receipt/v1`, not an
envelope wrapping it, so read `status`/`verified`/`changes` at the top level; and
a plan cannot declare its own risk level. The runner sets **L1** for every step
and check it sends, which is why a plan is the right shape for Row writes and
never for structure: `CREATE ROUTE` and `APPLY ROUTE MUTATION PLAN` are L2, and
they go through `exec` or `query` with an explicit level.

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
