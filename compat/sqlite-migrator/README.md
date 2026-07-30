# Isolated SQLite migrator

This compatibility module is not linked into Memora or its root `go.mod`.

```sh
cd compat/sqlite-migrator
go run . --source /absolute/instance/databases/prototype.sqlite \
  --target /absolute/instance/databases/database.memora
```

It opens the source read-only, retains `prototype.sqlite.pre-native.bak`, imports
through the logical snapshot contract, verifies the canonical hash, and only
then atomically publishes the target. Memora itself refuses to start on a
legacy-only instance, so SQLite can never become a silent runtime fallback.
