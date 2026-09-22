---
name: memora
description: memora 是用户的个人记忆与知识库。用户问到关于自己、项目、过往经历或个人相关的问题时，先在 memora 中查找；聊天中出现值得记录的新事实、决定、想法或任何有意义的内容时，存入 memora。本地查不到答案时再上网搜索；搜索到值得保留的内容也存入 memora。
---

# Memora

The user's personal memory and knowledge base, reached through one small SQL-like
language over a local daemon. This file carries only the constraints and the
index; every procedure lives in a reference you load when the task needs it.
**Read the reference before you act** — no complete statement is kept here,
because a spine that looks self-sufficient is how a host stops reading and starts
improvising.

## Constraints

- Only `memora doctor`, `query`, `exec`, `mutate` and `schema` do database work.
  A few commands exist for narrow cases and are used only that way:
  `daemon stop` (to clear a build skew the detector reports), `daemon status`,
  `parse` (to check a statement before sending it), `version` (the conventional
  `--version` flag is not accepted), and `instance destroy` (only on the user's
  explicit instruction).
- Never inspect, edit, copy or infer state from physical database, index, journal,
  page or instance files. Logical MSQL results are the host's only source of
  database truth.
- Every Database-specific `query` or `exec` carries one
  `memora.authorization/v2` object in `--input`: the host actor, the exact
  user-authorized Database names or stable IDs, and an explicit `default_level` —
  L0 for reads and plans, L1 for bounded reversible Row writes, L2 only for
  reviewed structural actions. `database_levels` overrides that per Database.
  Never widen scope or level to recover from `permission_denied`; an approval
  confirms a reviewed hash and never raises the granted level.
- Writes also print non-JSON notes to **stderr** (a partly configured embedding
  provider is the usual one, and it says the units stay not-ready). A host that
  parses stdout as JSON must never merge stderr into that stream;
  `MEMORA_EMBEDDING=off` silences the provider when the task needs no vectors.
- The model Provider belongs to the host, not to Memora: any user-configured
  compatible base URL and model is normal, and so is host-side configuration such
  as CC Switch. Never pass Provider base URLs, API keys or bearer tokens to
  `memora`, its database, logs, receipts, exports or command input.
- Keep live schemas, routes, candidates and rows out of this file — discover them
  from the current instance for each task. It targets `memora.msql.ast/v1` and
  consumes `memora.result/v1`.
- For controlled real-model evaluation, the adjacent `host-contract.json` fixes
  one host-independent natural-language Task, Database scope and budget.

## Load the reference the task needs

| The task is | Read first |
| --- | --- |
| installing, detecting or removing the instance; what Memora *is*; the Admin | [`references/install.md`](references/install.md) |
| finding the right Database and Table, then reading rows out of it | [`references/discover-and-read.md`](references/discover-and-read.md) |
| "where did we discuss X", keyword or vector recall, embeddings, rekey, repair | [`references/recall-and-vectors.md`](references/recall-and-vectors.md) |
| adding, revising, splitting or merging a Row | [`references/write.md`](references/write.md) |
| creating a Table or Column, or restructuring the semantic tree | [`references/schema-and-router.md`](references/schema-and-router.md) |
| a Row was deleted, or the derived layers drifted | [`references/recover.md`](references/recover.md) |
| what the product is meant to be, and its architecture | [`references/product-manual.md`](references/product-manual.md) |

Two rules about that index: the references are **procedure**, and they are longer
than this file on purpose — load the one the task needs rather than skimming all
of them; and when the question is already answered by what you have read, no
reference is needed at all.

When the question is "what did we decide", the memory is not the only source: the
repository's own docs carry the durable record too (`docs/decisions.md`,
`docs/planning/*`), and the two are meant to agree. Say which one you answered
from, and when they disagree, say that instead of picking one.
