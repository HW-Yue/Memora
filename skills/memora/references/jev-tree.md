# memora — Walk to a landing with jev

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

## What this is

One requirement goes in; what comes back is the set of **landings** — leaf
positions, each with the path that says what it is and the handle that reads it
back. It is the same kind of answer `RECALL` gives and it is not the same answer:
recall ranks by similarity and returns *candidates*, while this walks the tree and
returns positions the walk **committed to**. The set is unordered, carries no
score, and must not be read as a list to choose from. A set of one is not a
special case — `n` is simply how many places the requirement points at.

Every landing is `{database, table, path, leaf_route_id, row_id, revision}`:
**`path` is what you judge** ("is this the one?"), and **`(database, table,
row_id)` is what you read with** — a path alone cannot be written into a query,
and a row id alone does not say which Table to read. **The walk locates; you read
the fact.** It never returns content and never returns a branch: a branch nobody
walked goes to `stopped`.

A landing's **`revision` is the Row's** — the version of the fact, which is what a
later `UPDATE … WHERE row_id = :row` has to be given. It is **not** the Route
node's `revision`, which is the version of the *position* and moves when the node
is renamed or re-purposed while the fact under it is untouched. Both arrive on the
same `SHOW ROUTES` row, as `row_revision` and `revision`; passing one where the
other belongs is a revision conflict on an object nobody touched.

The same set also comes back grouped as `reads`:
`[{database, table, columns, rows:[{path, row_id, revision}]}]` — one entry per
Table that has landings, with the Table's column names, so **write one
`SELECT … WHERE row_id IN (…)` per Table** instead of one statement per landing
(measured: 35 landings across 6 Tables is 6 statements, not 35).

Candidates are offered with their `purpose` **and** their `aliases` — the words
the owner would search with, short terms beside the description. If you are
writing the tree, that is why aliases are worth filling in: the walk is the only
thing that reads them.

```sh
echo '{"requirement":"<what the user wants>","authorized_databases":["<db>","<db>"]}' \
  | python3 "<skill-directory>/scripts/jev_tree.py"
```

It walks three kinds of layer with one rule each:

1. **which Database** — the candidates are the authorized ones, from
   `SHOW DATABASES` read *with* an authorization object (discovery mode returns
   every Database and is never an input here);
2. **which Table** — `SHOW CATALOG ATLAS`, read one Database at a time. A
   requirement that points at several Databases may return several, and their
   Tables are then asked in a single question whose options carry the Database;
3. **the semantic tree** — `SHOW ROUTES`, layer by layer, breadth first. The
   listing carries each leaf's `row_id` and `row_revision`, so reaching a leaf is
   already reaching its Row — there is **no `OPEN ROUTE` per leaf**. That is one
   statement per layer instead of one per layer plus one per landing (measured:
   the widest walk went from 50 statements to about 15). If you walk the tree by
   hand rather than with this script, take the Row from the listing for the same
   reason.

Every layer is decided the same way: the children's `name` and `purpose` go to
`scripts/jev_select.py`, which answers with a set of names plus
`separated`/`undecided`/`empty` (see the recall reference for how that cut is
made — no probability leaves the model call). A layer with a single child is not
a decision and is not asked.

## One requirement, one topic

The walk carries one requirement across Databases, Tables and the tree, and it is
fine for that requirement to touch several places — a cross-library question is a
legal answer, not an error. What it cannot carry is **several questions at once**:
"rekey, my internships and the current gaps" is three requirements, and the layer
decisions get asked one blended question instead of three clear ones.

So: **one call per topic.** (This used to be blamed on a read-side width cap that
dropped branches by arrival order — that cap is gone; see
`docs/planning/whole-layer-read.md`. The topic rule survives on its own merits:
a blended question is a worse question, and `empty` pruning works better when the
intent is one thing.)

## When it is worth calling

- the requirement maps onto the tree's **structure** ("which of my internships",
  "where do we keep the decisions about X"), and
- you want a **reproducible, explainable** path — every layer can say what it
  chose between, and the whole run can be replayed (`--record`, `--replay`).

Do not use it when the requirement is **similarity** rather than structure
("everything about storage engines") — `RECALL` is for that and is far cheaper.
Do not use it for **counting or universal** questions ("how many", "all of
them"): a walk enumerates positions, it does not count. And when the user already
names the Database, the Table or the node, do not call it at all — pass
`"database"`/`"table"`, or just read what they named.

**It is not faster than recall.** A walk costs one jev call per layer with more
than one child (~1 s each) plus local statements, so a deep tree is seconds;
recall is one statement. What it buys is a landing that is right by construction
and an answer that says how it got there.

## The answer

```json
{
  "landings": [
    {"database": "work", "table": "experiences", "path": "/internship/ACME",
     "leaf_route_id": "route_…", "row_id": "row_…", "revision": 1,
     "termination": "leaf"}
  ],
  "incomplete": false,
  "incomplete_at": [],
  "undescribed_at": ["work:internship"],
  "pruned_at": [],
  "stopped": [],
  "evidence": [
    {"layer": "databases", "mode": "set",
     "options": [{"name": "work", "purpose": "…"}],
     "relevant": ["work"], "decision": "separated"}
  ],
  "decisions": 3,
  "elapsed_ms": 3563,
  "model": "jev-1.13.0"
}
```

- **Read the Row before answering.** A landing is a handle, never a fact: open it
  (or `SELECT … WHERE row_id = :row` with the landing's `database` and `table`).
- `termination` on a landing is always `leaf`. A branch is never a landing —
  anything the walk did not reach is in `stopped`, with its reason. (`no_root`
  appears in `evidence` when a Table has no semantic tree yet.)
- `incomplete` / `incomplete_at` name every layer the model answered without
  separating. Those layers are **enumerated** (the model made no filter worth
  trusting), which can make the answer wide — say so rather than presenting it as
  narrow.
- `evidence` carries the option text each decision was made from, so a decision
  can be audited or argued with later. It carries names and decisions, never
  probabilities. Its `layer` names carry the Database (`work:root`) because a bare
  `root` names one layer in each of them.
- `undescribed_at` names every layer that was chosen **from bare names**: on that
  layer at least one candidate's `purpose` was blank or only repeated its `name`
  (compared after folding case, width and padding), which is the same absence
  either way. Such a candidate is offered to jev **with an empty purpose** — the
  name is never copied into its place — and the layer's evidence entry lists the
  ones it means in `undescribed`. This is not a failure and not `incomplete`: the
  walk still decided, but it decided blind, so a thin or wide answer there is
  explained rather than mysterious. The repair is to write those `purpose`
  sentences ("what is kept here"), not to widen the requirement.
- `stopped` lists every branch the walk **never reached**, with the reason and the
  candidate count. A branch that was not walked is not a landing: it never appears
  in `landings`. `incomplete` covers both this and the enumerated layers, so an
  answer that is not whole cannot be mistaken for one that is.
- `pruned_at` names the layers jev pruned by answering `empty`. Pruning is a
  result, not a fault — but a silent prune is why a missing answer could never be
  attributed, so it is in the answer.
- `tree_damage` appears when a Route is reached twice: the tree has a cycle the
  write path refuses to build, so this is damaged data for `doctor`, not a depth
  budget running out.
- The **only** budget is a wall-clock valve (120 s) against a hung or retrying
  provider. There is no width, call or depth cap: a layer's size is the write
  path's business (`route_policy.branch_fanout`), the tree is finite, and `empty`
  prunes — so the walk finishes by itself. `jev_calls` is still reported because a
  metered path needs observing, but counting is not capping. When the valve bites,
  the branch goes to `stopped` — narrow the requirement, or name the
  Database/Table and read it directly.

## Reading a run

The walk says what it did while it does it. The same lines go to stderr and, with
`--log FILE`, to a file as JSON lines (`--quiet` silences stderr; stdout stays the
answer alone):

```sh
... | python3 "<skill-directory>/scripts/jev_tree.py" --log /tmp/jev-tree.jsonl
```

```
[     0 ms] start      requirement=… mode=live
[    10 ms] statement  layer=databases source=SHOW DATABASES rows=2 duration_ms=10
[  1836 ms] decision   layer=databases options=2 chosen=['work'] decision=separated provider_ms=1746
[ 2685 ms] skipped    layer=root reason=single child options=1 chosen=['internship']
[ 3516 ms] done       landings=2 decisions=3 statements=7 engine_ms=81 jev_ms=3427
```

A `decision` line also carries `undescribed=[…]` when candidates on that layer
arrived with nothing but their names, and the `done` line repeats the layers as
`undescribed_at`. A run where that list is long is a run whose layers were picked
from labels — read the answer as the guess it is, and say so to the user.

One real run of "both internships" reads like that: **1.6 s of 1.7 s is the three
model decisions** — the first ~0.8 s pays the TLS handshake and the next two
~0.35 s each, because the walk keeps **one** provider connection for all of them —
while the seven local statements cost ~120 ms in total. Rebuilding that connection
per decision (or per subprocess) was measured at ~0.73 s every time, which is
roughly what a walk with nine decisions spent before the connection was reused
(8.1 s, now 4.5 s). The log is also where a stop shows: a layer the wall-clock
valve cut is recorded as a `stopped` entry, never as a landing.

A kept connection can be closed by the far end between calls; the provider drops
it and tries once more, so the cost of reuse is one retry, never a failure.
`JEV_IN_PROCESS=0` falls back to one subprocess per decision — slower, and kept
only so a broken import cannot break the walk.

The answer carries the same numbers: `timings.engine_ms`, `timings.jev_ms`,
`statements`, `decisions`, and per layer `evidence[].options_count` and
`evidence[].elapsed_ms`. `--replay` reports the recorded run with `recorded: true`
and near-zero durations, which is how you tell "what it decided" from "what it
cost".

## Boundaries

- **Read-only.** Every statement is a read on the surfaces this path uses
  (`SHOW DATABASES`, `SHOW CATALOG ATLAS`, `SHOW ROUTES`, `DESCRIBE TABLE`), and
  each one carries an authorization object scoped to the single Database it reads.
  `OPEN ROUTE` is no longer among them — the layer listing already says which Row
  a leaf holds — but the statement itself still exists for readers that open a
  single known leaf.
- **Only authorized Databases are ever offered.** Not as candidates, not as
  negative examples: an unauthorized name in a model prompt is a leak, and it is
  also the first step of widening scope to make an answer fit.
- Writing is not in scope for this path. If a write needs "where does this
  belong", the same catalog question can inform it, but a write must land on
  exactly one position and the Skill's write procedure is the one that decides.
