"""End-to-end smoke of every command and statement the Memora Skill uses.

Usage: python3 scripts/prototype_smoke.py <memora binary> <fresh data dir with a running daemon>
"""
import json, subprocess, sys
M, D = sys.argv[1], sys.argv[2]
def cli(*args, expect_ok=True):
    out = subprocess.run([M, *args, "--data-dir", D], capture_output=True, text=True)
    if out.returncode != 0 and expect_ok:
        raise SystemExit(f"FAIL {args[0]}: rc={out.returncode}\nstdout={out.stdout[:1500]}\nstderr={out.stderr[:1500]}")
    try:
        return json.loads(out.stdout)
    except Exception:
        return out.stdout
def auth(level="L0", approval=None):
    a = {"version":"memora.authorization/v2","actor":"agent:host","authorized_databases":["work"],"default_level":level}
    if approval: a["approval"] = approval
    return a
def msql(kind, sql, named=None, mutation=None, level="L0", approval=None):
    payload = {"authorization": auth(level, approval)}
    if named is not None: payload["parameters"] = {"named": named}
    if mutation is not None: payload["mutation"] = mutation
    env = cli(kind, "--input", json.dumps(payload), sql)
    if not isinstance(env, dict) or not env.get("ok"):
        raise SystemExit(f"FAIL {sql}\n{json.dumps(env, ensure_ascii=False)[:1500]}")
    return env["results"][0]
def step(name): print("✓", name)

# 1. ensure schema
schema = {"version":"memora.schema-plan/v1","id":"schema-8","actor":"agent:host","source_event_id":"conversation:event-8","reason":"new durable project domain","authorized_databases":["work"],"ensure":{"database":{"name":"work","purpose":"Project knowledge","scope":"Reviewed projects"},"database_synonyms":["projects"],"table":{"name":"notes","purpose":"Durable decisions","row_semantics":"One reviewed decision","columns":[{"name":"title","type":"TEXT(200)","nullable":False,"purpose":"Decision title","semantic_role":"title"},{"name":"summary","type":"TEXT(4000)","nullable":True,"purpose":"Complete note","semantic_role":"summary"}]},"table_synonyms":["decisions"]}}
print(json.dumps(cli("schema", "--plan", json.dumps(schema)), ensure_ascii=False)[:300]); step("memora schema --plan")
atlas = msql("query", "SHOW CATALOG ATLAS LIMIT :limit BYTES :bytes COMPACT", {"limit":64,"bytes":8192}); step("SHOW CATALOG ATLAS")
table = msql("query", "DESCRIBE TABLE work.notes COMPACT")
sv = int(table["rows"][0]["schema_version"])

# 2. bootstrap router
w = lambda reason, **kw: {"expected_schema_version":sv,"max_affected_rows":1,"actor":"agent:host","source":"conversation:event-7","reason":reason, **kw}
root = msql("exec", "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose", {"purpose":"Semantic navigation root for work notes"}, w("bootstrap router"), "L2")["rows"][0]["route_id"]
arch = msql("exec", "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose", {"parent":root,"name":"architecture","kind":"branch","purpose":"Architecture decisions"}, w("group"), "L2")["rows"][0]["route_id"]
archive = msql("exec", "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose", {"parent":root,"name":"archive","kind":"branch","purpose":"Superseded decisions"}, w("group"), "L2")["rows"][0]["route_id"]
storage = msql("exec", "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose", {"parent":arch,"name":"storage","kind":"leaf","purpose":"Storage engine choice"}, w("leaf"), "L2")["rows"][0]["route_id"]
step("bootstrap router (root/branch/leaf)")

# 3. mutate INSERT plan
plan = {"version":"memora.mutation-plan/v1","id":"plan-insert-1","decision":"INSERT","database":"work","table":"notes","actor":"agent:host","source_event_id":"conversation:event-7","reason":"record storage decision","authorized_databases":["work"],
  "preflight":[{"id":"leaf-empty","msql":"OPEN ROUTE :leaf LIMIT 1","input":{"parameters":{"named":{"leaf":storage}}},"expect_rows":0}],
  "steps":[{"id":"insert","kind":"INSERT","target":"work.notes","msql":"INSERT INTO work.notes (title, summary) VALUES (:title, :summary)","input":{"parameters":{"named":{"title":"Use SQLite","summary":"SQLite with sqlite-vec replaces the native engine."}},"mutation":{"expected_schema_version":sv,"max_affected_rows":1,"route_leaf_ids":[storage],"actor":"agent:host","source":"conversation:event-7","reason":"record storage decision","source_kind":"conversation_assertion"}}}],
  "verify":[{"id":"leaf-filled","msql":"OPEN ROUTE :leaf LIMIT 1","input":{"parameters":{"named":{"leaf":storage}}},"expect_rows":1}]}
receipt = cli("mutate", "--plan", json.dumps(plan)); print(json.dumps(receipt, ensure_ascii=False)[:400]); step("memora mutate --plan INSERT")
row = msql("query", "OPEN ROUTE :leaf LIMIT 1", {"leaf":storage})["rows"][0]["row_id"]
msql("query", "SELECT title, summary, row_id, revision FROM work.notes WHERE row_id = :row LIMIT :limit", {"row":row,"limit":10}); step("OPEN ROUTE -> SELECT")
msql("exec", "UPDATE work.notes SET summary = :summary WHERE row_id = :row", {"row":row,"summary":"SQLite (WAL) + vec0."}, w("refine", expected_revision=1), "L1"); step("UPDATE with expected_revision")
msql("query", "SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 20", {"row":row}); step("SHOW HISTORY")
msql("query", "SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :q LIMIT :l BYTES :b", {"q":"storage engine","l":8,"b":4096}); step("ROUTE CANDIDATES LEXICAL")
msql("query", "SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query LIMIT :l BYTES :b", {"query":"sqlite","l":10,"b":8192}); step("LEXICAL LOCATIONS")

# 4. route mutation MOVE with approval
node = msql("query", "DESCRIBE ROUTE :r", {"r":storage})["rows"][0]
proposal = {"version":"memora.route-mutation-proposal/v1","proposal_id":"route-proposal-1","operation":"MOVE","actor":"agent:host","source_event_id":"conversation:event-9","reason":"move reviewed leaf","sources":[{"route_id":storage,"expected_revision":int(node["revision"])}],"target_parent_id":archive}
planned = msql("query", "PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal", {"proposal":proposal})["rows"][0]
rplan = planned["route_mutation_plan"]
approval = {"version":"memora.approval/v1","action":"APPLY_ROUTE_MUTATION","subject_sha256":rplan["hash"].removeprefix("sha256:"),"confirmed":True}
msql("exec", "APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes", {"plan":rplan}, None, "L2", approval); step("PLAN/APPLY ROUTE MUTATION (MOVE)")
under = msql("query", "SHOW ROUTES UNDER :p LIMIT 12", {"p":archive})["rows"]
assert [r["route_id"] for r in under] == [storage], under

# 5. schema change plan with approval
cols = msql("query", "SHOW COLUMNS FROM work.notes LIMIT 32")["rows"]
title = [c for c in cols if c["name"]=="title"][0]
table = msql("query", "DESCRIBE TABLE work.notes")["rows"][0]
sp = {"version":"memora.schema-change-proposal/v1","proposal_id":"schema-proposal-9","actor":"agent:host","source_event_id":"conversation:event-9","reason":"add status","expected_table_revision":int(table["schema_version"]),"changes":[{"change_id":"add-status","action":"ADD_COLUMN","definition":{"name":"status","type":"TEXT(40)","nullable":True,"purpose":"Decision status","semantic_role":"status"}}]}
splan = msql("query", "PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal", {"proposal":sp})["rows"][0]
sp_plan = [v for k,v in splan.items() if isinstance(v, dict) and "hash" in v][0]
approval = {"version":"memora.approval/v1","action":"APPLY_SCHEMA_CHANGE","subject_sha256":sp_plan["hash"].removeprefix("sha256:"),"confirmed":True}
msql("exec", "APPLY SCHEMA CHANGE PLAN :plan FOR TABLE work.notes", {"plan":sp_plan}, None, "L2", approval); step("PLAN/APPLY SCHEMA CHANGE")

# 6. host workflow commands
print(json.dumps(cli("capture", "--candidate", json.dumps({"version":"memora.host-input/v1","input_id":"input-12","workspace":"project-memora","actor":"agent:host","authorized_databases":["work"],"candidate_text":"Router results are locators, not facts.","source":{"kind":"conversation_assertion","title":"Router boundary"}})), ensure_ascii=False)[:300]); step("memora capture")
pending = cli("capture", "--receipt", "input-12", "--workspace", "project-memora"); step("memora capture --receipt")
print(json.dumps(cli("reflect", "--event", json.dumps({"version":"memora.conversation-event/v1","event_id":"checkpoint-9","session_id":"host-session-2","kind":"checkpoint","workspace":"project-memora","authorized_databases":["work"],"checkpoint":{"active_database":"work","route_path":"/architecture","last_event_id":"event-8"}})), ensure_ascii=False)[:300]); step("memora reflect")
print(json.dumps(cli("feedback", "--event", json.dumps({"version":"memora.feedback-event/v1","event_id":"feedback-10","kind":"wrong","actor":"agent:host","reason":"user says the summary is wrong","target":{"database":"work","table":"notes","row_id":row,"revision":2}})), ensure_ascii=False)[:300]); step("memora feedback")
print(json.dumps(cli("maintain", "--report"), ensure_ascii=False)[:300]); step("memora maintain --report")
out = subprocess.run([M, "assimilate", "--event", json.dumps({"version":"memora.assimilation-event/v1","event_id":"book-status-2","task_id":"book-task","workspace":"project-memora","kind":"status"}), "--data-dir", D], capture_output=True, text=True)
assert "not_found" in out.stderr, out.stderr  # status of an unknown task is a clean not_found
step("memora assimilate --event (unknown task -> not_found)")
print(json.dumps(cli("doctor"), ensure_ascii=False)); step("memora doctor")
print("ALL SKILL COMMANDS OK")
