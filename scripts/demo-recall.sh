#!/usr/bin/env bash
# Build a small semantic tree, embed it with the host's real provider, and let
# both recall arms answer. This is an opt-in demo, NOT part of the gate: it calls
# a model API, which ordinary tests must never do (docs/development/testing.md).
#
# The provider comes from the environment; if it is not exported here, the script
# reads the assignments out of ${MEMORA_ENV_FILE:-~/.zshrc} without ever printing
# a value.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
binary=${MEMORA_BIN:-$root/memora}
INSTANCE=${MEMORA_DEMO_DIR:-$(mktemp -d)/instance}
ENV_FILE=${MEMORA_ENV_FILE:-$HOME/.zshrc}

provider_value() {
  # Values already exported win; otherwise read the assignment out of the profile
  # (a plain line scan — no shell evaluation of the user's rc file).
  local name=$1 current=${!1:-}
  if [[ -n "$current" ]]; then printf '%s' "$current"; return; fi
  [[ -f "$ENV_FILE" ]] || return 0
  # grep -E, not sed: BSD sed has no \+ in a basic regex, so an "export " prefix
  # would silently never match on macOS.
  grep -E "^[[:space:]]*(export[[:space:]]+)?${name}=" "$ENV_FILE" | tail -1 \
    | sed -E "s/^[[:space:]]*(export[[:space:]]+)?${name}=//" \
    | sed -E 's/^"(.*)"$/\1/; s/^'"'"'(.*)'"'"'$/\1/' \
    | tr -d '\r' | sed -E 's/[[:space:]]+$//' 
}

BASE_URL=$(provider_value MEMORA_EMBEDDING_BASE_URL)
MODEL=$(provider_value MEMORA_EMBEDDING_MODEL)
DIMENSIONS=$(provider_value MEMORA_EMBEDDING_DIMENSIONS)
API_KEY=$(provider_value MEMORA_EMBEDDING_API_KEY)
for pair in "MEMORA_EMBEDDING_BASE_URL:$BASE_URL" "MEMORA_EMBEDDING_MODEL:$MODEL" \
            "MEMORA_EMBEDDING_DIMENSIONS:$DIMENSIONS" "MEMORA_EMBEDDING_API_KEY:$API_KEY"; do
  if [[ -z "${pair#*:}" ]]; then
    printf 'demo: %s is not set (checked the environment and %s)\n' "${pair%%:*}" "$ENV_FILE" >&2
    exit 2
  fi
done
export MEMORA_EMBEDDING_BASE_URL=$BASE_URL MEMORA_EMBEDDING_MODEL=$MODEL
export MEMORA_EMBEDDING_DIMENSIONS=$DIMENSIONS MEMORA_EMBEDDING_API_KEY=$API_KEY

# Always build: a stale binary from an earlier run fails in ways that look like
# engine bugs, which is a waste of everybody's afternoon.
printf 'demo: building the CLI (with sqlite_fts5, which this tree requires)\n' >&2
(cd "$root" && go build -tags sqlite_fts5 -o "$binary" ./cmd/memora)

TMP=${MEMORA_DEMO_TMP:-$(mktemp -d)}
mkdir -p "$TMP"
cleanup() { "$binary" daemon stop --data-dir "$INSTANCE" >/dev/null 2>&1 || true; }
trap cleanup EXIT

mkdir -p "$INSTANCE"
"$binary" init --data-dir "$INSTANCE" >/dev/null
"$binary" daemon start --data-dir "$INSTANCE" >/dev/null
sleep 1.5

# --- the semantic tree -------------------------------------------------------
# One branch per domain, one leaf per fact. A Row occupies exactly one leaf, so
# the tree is the only statement of where anything is.
python3 - "$TMP" "$MODEL" "$DIMENSIONS" <<'PY'
import json, pathlib, sys
tmp, model, dimensions = pathlib.Path(sys.argv[1]), sys.argv[2], int(sys.argv[3])
A2 = {"authorization": {"version": "memora.authorization/v2", "actor": "agent:host",
                        "authorized_databases": ["life"], "default_level": "L2"}}
A1 = {**A2, "authorization": {**A2["authorization"], "default_level": "L1"}}
A0 = {**A2, "authorization": {**A2["authorization"], "default_level": "L0"}}
SEED = {"mutation": {"expected_schema_version": 1, "max_affected_rows": 8,
                     "actor": "agent:host", "source": "demo", "reason": "seed"}}
TREE = [
    (["技术", "数据库", "存储引擎"], "自研页式引擎的维护成本高于收益，页格式、WAL 与崩溃恢复交给 SQLite。"),
    (["技术", "数据库", "向量检索"], "向量只用来定位，从不产出事实；召回返回路径而不返回分数。"),
    (["技术", "编程语言", "Go"], "cgo 让 Go 能编进 C 扩展，可选模块由构建标签控制。"),
    (["生活", "健康"], "每周三次慢跑，睡眠优先于加班。"),
    (["生活", "阅读"], "《设计数据密集型应用》读到复制与分区。"),
    (["工作", "Memora"], "个人语义数据库：Agent 自主建模，一切都落成普通 SQLite 表。"),
]
def write(name, obj): (tmp / name).write_text(json.dumps(obj))
write("db.json", A2); write("table.json", A2); write("root.json", {**SEED, **A2}); write("read.json", A0)
for index, (path, fact) in enumerate(TREE):
    segments = [{"name": name, "kind": "branch" if position < len(path) - 1 else "leaf",
                 "purpose": name} for position, name in enumerate(path)]
    mutation = dict(SEED["mutation"]); mutation["route_path"] = segments
    write(f"insert{index}.json", {"parameters": {"named": {"t": fact}}, "mutation": mutation, **A1})
(tmp / "tree.json").write_text(json.dumps([{"path": p, "fact": f} for p, f in TREE]))
PY

run() { "$binary" exec --data-dir "$INSTANCE" --input "$(cat "$1")" "$2"; }
query() { "$binary" query --data-dir "$INSTANCE" --input "$(cat "$1")" "$2"; }
say() { python3 -c "import sys,json;d=json.load(sys.stdin);r=d['results'][0];print('   $1:', r['error']['message'] if not d['ok'] else '$2')"; }

echo "── 建树（一个域一个分支，一条事实一个叶子）"
run "$TMP/db.json" "CREATE DATABASE life PURPOSE '个人知识' SCOPE '技术与生活'" | say "建库" ok
run "$TMP/table.json" "CREATE TABLE life.notes PURPOSE '一条事实' ROW SEMANTICS '一条可召回的事实' (title TEXT NOT NULL PURPOSE '事实' ROLE title)" | say "建表" ok
run "$TMP/root.json" "CREATE ROUTE ROOT FOR TABLE life.notes PURPOSE '全部个人知识'" | say "建根" ok
for index in 0 1 2 3 4 5; do
  run "$TMP/insert$index.json" "INSERT INTO life.notes (title) VALUES (:t)" | say "写入 $(python3 -c "import json;print(json.load(open('$TMP/tree.json'))[$index]['path'][-1])")" "ok（向量由 CLI 自动算好附上）"
done

echo "── 待办清单（配了 provider 就该是空的）"
python3 - "$TMP" >"$TMP/pending.json" <<'PY'
import json, sys, pathlib
tmp = pathlib.Path(sys.argv[1])
base = json.load(open(tmp / "db.json"))
base["authorization"]["default_level"] = "L0"
base["parameters"] = {"named": {"limit": 32}}
print(json.dumps(base))
PY
query "$TMP/pending.json" "SHOW PENDING VECTORS IN DATABASE life LIMIT :limit" \
  | python3 -c "import sys,json;d=json.load(sys.stdin);print('   未就绪单元:', len(d['results'][0]['rows']))"

echo "── 语义索引路（Agent 主路：逐层选，再回表）"
python3 - "$binary" "$INSTANCE" "$TMP" <<'PY'
import json, pathlib, subprocess, sys
binary, instance, tmp = sys.argv[1], sys.argv[2], pathlib.Path(sys.argv[3])

def call(source, named=None, write=False):
    base = json.load(open(tmp / ("db.json" if write else "read.json")))
    if named:
        base["parameters"] = {"named": named}
    command = "exec" if write else "query"
    done = subprocess.run([binary, command, "--data-dir", instance, "--input", json.dumps(base), source],
                          capture_output=True, text=True)
    result = json.loads(done.stdout)["results"][0]
    if result.get("error"):
        raise SystemExit("  %s -> %s" % (source, result["error"]["message"]))
    return result["rows"]

print("  ① 发现：", ", ".join(row["name"] for row in call("SHOW TABLES FROM life LIMIT 10 COMPACT")))

level = call("SHOW ROUTES FROM TABLE life.notes AT ROOT LIMIT 12")
print("  ② 根下分支：", ", ".join("%s(%s)" % (row["name"], row["kind"]) for row in level))
chosen = next(row for row in level if row["name"] == "技术")

level = call("SHOW ROUTES UNDER :parent LIMIT 12", {"parent": chosen["route_id"]})
print("  ③ 技术下：", ", ".join("%s(%s)" % (row["name"], row["kind"]) for row in level))
chosen = next(row for row in level if row["name"] == "数据库")

level = call("SHOW ROUTES UNDER :parent LIMIT 12", {"parent": chosen["route_id"]})
print("  ④ 数据库下：", ", ".join("%s(%s)" % (row["name"], row["kind"]) for row in level))
leaf = next(row for row in level if row["name"] == "存储引擎")

locator = call("OPEN ROUTE :leaf LIMIT 1", {"leaf": leaf["route_id"]})
rowID = locator[0]["row_id"]
print("  ⑤ 打开叶子 -> row_id=%s" % rowID)

rows = call("SELECT title, row_id, revision FROM life.notes WHERE row_id = :row LIMIT 1", {"row": rowID})
for row in rows:
    print("  ⑥ 回表取事实：%s（revision %s）" % (row["title"], row["revision"]))
PY

echo "── 召回演示"
python3 - "$TMP" "$BASE_URL" "$MODEL" "$DIMENSIONS" "$API_KEY" <<'PY'
import base64, json, pathlib, struct, sys, urllib.request
tmp, base_url, model, dimensions, key = pathlib.Path(sys.argv[1]), *sys.argv[2:6]
def embed(text):
    request = urllib.request.Request(
        base_url.rstrip("/") + "/embeddings",
        data=json.dumps({"model": model, "input": [text], "dimensions": int(dimensions)}).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + key})
    with urllib.request.urlopen(request, timeout=60) as response:
        vector = json.loads(response.read())["data"][0]["embedding"]
    return base64.urlsafe_b64encode(struct.pack("<%df" % len(vector), *vector)).decode().rstrip("=")
for name, text in [("keyword.json", "SQLite"),
                   ("vector.json", "怎么让机器记住我说过的话"),
                   ("union.json", "崩溃恢复 与 记忆")]:
    base = json.load(open(tmp / "db.json"))
    base["authorization"]["default_level"] = "L0"
    if name == "keyword.json":
        base["parameters"] = {"named": {"q": text}}
        statement = "RECALL FROM life MATCH :q LIMIT 5"
    elif name == "vector.json":
        base["parameters"] = {"named": {"v": embed(text)}}
        statement = "RECALL FROM life NEAREST :v LIMIT 5"
    else:
        base["parameters"] = {"named": {"q": text, "v": embed(text)}}
        statement = "RECALL FROM life MATCH :q NEAREST :v LIMIT 5"
    (tmp / (name.removesuffix(".json") + ".stmt")).write_text(statement)
    (tmp / name).write_text(json.dumps(base))
PY
for demo in keyword vector union; do
  label=$(python3 -c "print({'keyword':'① 关键词 MATCH','vector':'② 向量 NEAREST','union':'③ 并集 MATCH+NEAREST'}['$demo'])")
  python3 -c "print('   $label')"
  query "$TMP/$demo.json" "$(cat "$TMP/$demo.stmt")" | python3 -c "
import sys, json
result = json.load(sys.stdin)['results'][0]
if not result['rows']:
    print('      （无命中）', result.get('error', {}).get('message', ''))
for row in result['rows']:
    print('      ' + ' → '.join(segment['name'] for segment in row['path']))
if result['warnings']:
    print('      warnings:', [w['code'] for w in result['warnings']])"
done
echo "── 完成（实例留在 ${INSTANCE}）"
