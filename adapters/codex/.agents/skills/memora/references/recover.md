# memora — Recover an archived deletion

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

## Recover an archived deletion

A DELETE removes the Row, the leaf it occupied, its history and both ends of its
links. The engine writes one archive record first, and rebuilding from it is your
work, not the engine's.

```sh
memora query --input '{"parameters":{"named":{"row":"row_01","limit":10}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "SHOW ARCHIVE FROM work.notes FOR ROW :row LIMIT :limit"
memora query --input '{"parameters":{"named":{"archive":"archive_01"}},"authorization":{"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":"L0"}}' "OPEN ARCHIVE :archive"
```

`SHOW ARCHIVE` lists metadata only and requires both a Table scope and a LIMIT.
Leaving `FOR ROW` out lists the whole Table's deletions, which is the only way to
discover a deletion when you no longer know its `row_id` — the `FOR ROW` form
presupposes you do. Its `archive_id` is what the second statement takes: there is
no other way to name an archive record. `OPEN ARCHIVE` returns one record in full — the path
root-first, and the Row as it was stored, including the links it carried. The
archived values are keyed by **column_id**, not by column name, so rebuilding
means mapping them back through `DESCRIBE TABLE`; and the archived record's
`row_state` still reads `live`, because that is the state the Row was in when it
was archived — it is a record of what was, not of what is now. A deleted Row is unreachable
everywhere else (`SELECT`, `SHOW HISTORY`, `AS OF`, `OPEN ROUTE`); the archive is
the single exception, and `SELECT` cannot reach it either. The `AS OF` forms are
`SELECT … AS OF REVISION :revision WHERE row_id = :row LIMIT 1` and
`SELECT … AS OF COMMIT_SEQUENCE :sequence WHERE row_id = :row LIMIT 1` — the
version keyword is not optional. An `AS OF` read answers for a Row that is live
now, and its Rows come back without the attached fields: `links` is `null` and
`route_paths` is `[]`, because those describe the Row's present placement and an
old revision may sit somewhere else today. Rebuild by recreating
the path (`CREATE ROUTE`, or `route_path` on the INSERT) and mounting the new Row
on its leaf. The archived IDs are a record of what was, not a promise it can be
reused.
## License

Memora is free for uses allowed by the
[PolyForm Noncommercial License 1.0.0](https://polyformproject.org/licenses/noncommercial/1.0.0).
Commercial use requires a separate written, paid commercial license.

Required Notice: Copyright 2026 HW-Yue. Commercial use requires a separate paid commercial license from the copyright holder. Commercial licensing inquiries: https://github.com/HW-Yue/Memora
