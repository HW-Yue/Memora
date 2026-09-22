# memora — Walk to a landing with jev

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

## What this is

One requirement goes in; what comes back is the set of **landings** — semantic
positions the requirement points at. It is the same kind of answer `RECALL` gives
and it is not the same answer: recall ranks by similarity and returns
*candidates*, while this walks the tree and returns positions the walk
**committed to**. The set is unordered, carries no score, and must not be read as
a list to choose from. A set of one is not a special case — `n` is simply how
many places the requirement points at.

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
3. **the semantic tree** — `SHOW ROUTES`, layer by layer, breadth first.

Every layer is decided the same way: the children's `name` and `purpose` go to
`scripts/jev_select.py`, which answers with a set of names plus
`separated`/`undecided`/`empty` (see the recall reference for how that cut is
made — no probability leaves the model call). A layer with a single child is not
a decision and is not asked.

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
    {"path": "/internship/ACME", "leaf_route_id": "route_…", "row_id": "row_…",
     "revision": 1, "termination": "leaf"}
  ],
  "incomplete": false,
  "incomplete_at": [],
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

- **Read the Row before answering.** A landing is a position, never a fact.
- `termination` says how the walk ended there: `leaf` (a Row is mounted),
  `no_root` (the Table has no semantic tree yet), or a `budget:` reason — the
  walk stopped, and the landing is the branch it stopped at.
- `incomplete` / `incomplete_at` name every layer the model answered without
  separating. Those layers are **enumerated** (the model made no filter worth
  trusting), which can make the answer wide — say so rather than presenting it as
  narrow.
- `evidence` carries the option text each decision was made from, so a decision
  can be audited or argued with later. It carries names and decisions, never
  probabilities.
- The budgets are constants, not knobs: 12 jev calls, depth 5, frontier width 4,
  30 seconds. When one bites, the answer says so — narrow the requirement, or
  name the Database/Table and read it directly.

## Boundaries

- **Read-only.** Every statement is a read on the surfaces this path uses
  (`SHOW DATABASES`, `SHOW CATALOG ATLAS`, `SHOW ROUTES`, `OPEN ROUTE`), and each
  one carries an authorization object scoped to the single Database it reads.
- **Only authorized Databases are ever offered.** Not as candidates, not as
  negative examples: an unauthorized name in a model prompt is a leak, and it is
  also the first step of widening scope to make an answer fit.
- Writing is not in scope for this path. If a write needs "where does this
  belong", the same catalog question can inform it, but a write must land on
  exactly one position and the Skill's write procedure is the one that decides.
