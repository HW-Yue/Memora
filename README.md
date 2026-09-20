# Memora（SQLite 原型分支）

> 分支 `rewrite/adr0011`：底层是 **SQLite**。现役是 Catalog、数据表、语义树与
> `SHOW ROUTES` / `SELECT`。设计依据见
> [ADR-0011](./docs/decisions/0011-pure-storage-engine-tables-everything.md)。

Memora 是一个给 AI Agent 用的本地个人数据库：Agent 自己建模、用 MSQL 读写，
通过**语义树**逐层找到数据，再按 RowID 回表取事实。

## 三分钟跑起来

需要 Go 1.25 与 C 编译器（SQLite 通过 cgo 编译，macOS 自带 clang 即可）。

```bash
CGO_ENABLED=1 CGO_CFLAGS="-Wno-deprecated-declarations" go build -o bin/memora ./cmd/memora
```

```bash
bin/memora init
```

```bash
bin/memora daemon start
```

```bash
bin/memora doctor
```

默认实例目录是 `~/Library/Application Support/Memora/instances/default`，
可以用 `--data-dir /绝对路径` 指定。数据库就是其中的 `databases/memora.db`，
任何 SQLite 工具都能打开查看。

## 第一次写入与查询

所有请求都带授权（`authorization`）；写入还要带出处（`mutation`）。

```bash
AUTH='"authorization":{"version":"memora.authorization/v2","actor":"agent:me","authorized_databases":["work"],"default_level":"L2"}'
bin/memora exec --input "{$AUTH}" "CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects and decisions'"
bin/memora exec --input "{$AUTH}" "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One reviewed fact' (title TEXT NOT NULL PURPOSE 'Title' ROLE title, body TEXT PURPOSE 'Body' ROLE summary)"
```

建语义树、挂数据：

```bash
M='"mutation":{"actor":"agent:me","source":"readme","reason":"setup","max_affected_rows":1,"expected_schema_version":1}'
bin/memora exec --input "{\"parameters\":{\"named\":{\"p\":\"All work knowledge\"}},$M,$AUTH}" "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :p"
bin/memora exec --input "{\"parameters\":{\"named\":{\"parent\":\"<root route_id>\",\"name\":\"architecture\",\"kind\":\"leaf\",\"purpose\":\"Architecture decisions\"}},$M,$AUTH}" "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose"
```

Agent 的查询路径（语义索引是现役主路）：

```text
SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12
SHOW ROUTES UNDER :branch LIMIT 12
OPEN ROUTE :leaf LIMIT 1                       -- 得到 row_id
SELECT * FROM work.notes WHERE row_id = :row LIMIT 1   -- 只有 SELECT 是事实
```

产品上还有关键词召回、向量召回和 Skill 层 jev，见
[查询形态](./docs/product/query-model.md)。

## 接入 Agent

- **Skill**：`skills/memora`，安装方式见 `skills/memora/scripts/install.sh`。
- **MCP**：`memora mcp` 提供单一工具 `memora_execute`，接 Claude Code / Codex。
- **Admin 控制台**：`memora admin --scope work` 打开本地只读控制台（目录、语义树画布、变更时间线、路由轨迹）。

## 命令

| 命令 | 用途 |
|---|---|
| `init` / `daemon start\|stop\|status\|run` / `doctor` | 实例与守护进程 |
| `query` / `exec` / `parse` | 执行或解析 MSQL |
| `mutate` / `schema` | 执行带预检与验证的写入计划、Schema 计划 |
| `mcp` / `admin` / `version` | 接入、控制台 |

## 存储里有什么

全部是 SQLite 普通表：

| 表 | 内容 |
|---|---|
| `mem_databases` / `mem_tables` | Catalog；每张数据表带角色（data / history / routes），Agent 只看得到 data |
| `data_<table_id>` | 数据行：值（按列 ID 存）、revision、`route_leaf_ids`、`links`、`successor_ids` |
| `history_<table_id>` | 该表每一次原地修改的完整版本；`SHOW HISTORY`、`AS OF` 读这里 |
| `routes_<table_id>` | 语义树节点：`parent_id` + `child_ids`，叶子挂 `row_id`；删除只置 `deprecated` 并记接替者 |
| `mem_changes` | 已提交事务的变更信封（谁、为什么、改了什么） |
| `mem_config` / `mem_traces` / `mem_kv` | 配置版本、路由轨迹、宿主工作流状态 |

写入串行，读取总是看到最近一次提交，不做 MVCC。拆分/合并数据行时，旧行标记为
superseded 并记录 `successor_ids`；引用按需跟随接替者（懒更新）。

## 许可

PolyForm Noncommercial，见 [LICENSE](./LICENSE)；商业授权见 [COMMERCIAL-LICENSE.md](./COMMERCIAL-LICENSE.md)。
