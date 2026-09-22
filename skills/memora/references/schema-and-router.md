# memora — Evolve schemas and restructure the Route tree

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

## Evolve schemas

Before creating a domain, discover existing Database and Table names and aliases.
Submit the proposed name plus a short explicit synonym set through one
`memora.schema-plan/v1` ensure plan, using the fixed template in "Decide where
knowledge lives". Database purpose/scope, Table purpose/row_semantics, and the
two fixed Columns are mandatory; reuse an exact candidate or alias, and do not
infer equivalence from a name alone.

**A Schema change is never how you add a field.** The only Column change this
Skill asks for on its own is **widening `summary`** when a document genuinely
does not fit its TEXT ceiling — an `ALTER_COLUMN` on `col_summary` with the same
name, purpose and role and a larger `TEXT(n)`. Everything else about a Table's
shape is the engine's, and a change you cannot express as "widen `summary`" is a
change you should not make: put the information in the Route tree, in the
`summary` prose, or in `links` instead.

For that widening — or for retiring Columns that older Tables carry — inspect
the exact Table/Column IDs and revisions, then submit explicit
`ALTER_COLUMN` (or `DROP_COLUMN` for a legacy Column the user has asked you to
retire) intent as `memora.schema-change-proposal/v1` through read-only MSQL.
Never use ad hoc `ALTER` statements or the older host rename runner:

```sh
memora query --input '{"parameters":{"named":{"proposal":{"version":"memora.schema-change-proposal/v1","proposal_id":"schema-proposal-9","actor":"agent:host","source_event_id":"conversation:event-9","reason":"widen the summary ceiling for a longer document","expected_table_revision":4,"changes":[{"change_id":"summary-ceiling","action":"ALTER_COLUMN","column_id":"col_summary","expected_revision":2,"definition":{"name":"summary","type":"TEXT(5000)","nullable":false,"purpose":"完整自足的文档正文","semantic_role":"summary"}}]}}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal"
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
   current limit, and merging them would lose a real distinction. Read the current
   value with `SHOW CONFIGURATION ROUTE_POLICY` (it is one policy for the
   **instance**, not one per Database), then raise it: `branch_fanout` lives in
   `2..100`, and one change may raise it by at most 4, so widening is a repeated
   decision rather than one jump to the ceiling. The statement is only half the
   write — the input carries the `expected_revision` from that read, plus actor
   and reason, without which the engine refuses it:

```sh
memora exec --input '{"parameters":{"named":{"fanout":16}},"mutation":{"expected_revision":1,"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-9","reason":"this instance really has more parallel groups"},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L1"}}' "ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :fanout"
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

Every node named in a proposal is `{"route_id": …, "expected_revision": …}`, and
each operation requires its own fields — the engine refuses a proposal that
carries the wrong shape for its `operation`:

| `operation` | required | forbidden |
| --- | --- | --- |
| `MOVE` | `sources`, `target_parent_id` | — |
| `SPLIT` | exactly one `sources` entry, at least two `targets` | `target_parent_id` |
| `MERGE` | at least two `sources` entries, exactly one `target` | `target_parent_id` |

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
