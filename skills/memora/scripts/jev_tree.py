#!/usr/bin/env python3
"""Walk databases -> tables -> the semantic tree with jev, and return landings.

This is a retrieval **path**, not a locator: one requirement goes in and what
comes out is the set of positions the requirement points at. It is the same kind
of answer `RECALL` gives, with one difference: recall ranks by similarity and
returns candidates, while this descends the tree and returns **landings**, each
one a position the walk committed to. The set is unordered, carries no score, and
must not be read as a candidate list (see `references/jev-tree.md`).

Why a script owns the descent: the alternative is the agent reading each layer
and choosing, which costs a model turn and a tool round trip per layer and puts
every layer listing into its context. Here the decisions are made by jev inside
one process, the layer invariants (floor probe, a relative cut, the budgets) stay
in code where they can be tested, and a whole run can be replayed from a
recording.

Contract
--------
    stdin : {"requirement": "<what the user wants>",
             "authorized_databases": ["<db>", ...],
             "database": "<db>",          # optional: skip the Database decision
             "table": "<table>"}          # optional: skip the Table decision
    stdout: {"requirement": ..., "landings": [{"database", "table", "path",
             "leaf_route_id", "row_id", "revision", "termination"}],
             "incomplete": bool, "incomplete_at": [...],
             "undescribed_at": [...], "evidence": [...], "jev_calls": n,
             "elapsed_ms": ms, "model": ...}
    exit  : 0 answered (including "nothing matched") | 2 not configured
            | 3 the provider refused | 4 bad input

A landing's `revision` is the **Row's** revision, which is what a later
`UPDATE … WHERE row_id = :row` has to be given. It is not the Route node's
`revision`: that one is the version of the position, and it moves when the node
is renamed or re-purposed while the fact under it is untouched. Both arrive on
the same `SHOW ROUTES` row, as `row_revision` and `revision` respectively.

Read-only: every statement carries an authorization object scoped to the one
Database being read, and nothing here writes.

Three statements per requirement plus one per layer: `SHOW DATABASES`, one
`SHOW CATALOG ATLAS` per Database, and one `SHOW ROUTES` per layer walked. There
is no `OPEN ROUTE` per leaf — the layer listing carries each leaf's Row.

`--record FILE` writes every engine answer and every jev answer it saw;
`--replay FILE` runs the same walk from that recording with no database and no
provider, which is how this path is tested in CI. `--log FILE` writes the step
log — every statement, every decision, what each chose and how long it took — as
JSON lines, while the same lines go to stderr unless `--quiet` is given. stdout
stays the answer alone.
"""

import argparse
import json
import os
import subprocess
import sys
import time
import unicodedata

# The sibling script that owns the question and the cut is importable, so a walk
# can ask it in-process and keep one provider connection for the whole run. It is
# a script, not a package, so the directory it lives in goes on the path first —
# and the interpreter is told not to leave a `__pycache__` behind: the Skill's
# tree is shipped and compared byte for byte, so an import must not add a file to
# it.
sys.dont_write_bytecode = True
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import jev_select  # noqa: E402  (after the path insert, deliberately)


class StepLog:
    """What the walk did, step by step: which layer, what it chose between, what
    it chose, and how long that took.

    Two consumers: a human reads the lines on stderr while the walk runs, and
    `--log FILE` writes the same events as JSON lines so a run can be analysed
    afterwards. stdout stays the answer alone — a host parses that, and a note
    mixed into it is how a JSON stream breaks.
    """

    def __init__(self, path, quiet):
        self.started = time.monotonic()
        self.path = path
        self.quiet = quiet
        self.events = []

    def emit(self, kind, **fields):
        event = {"seq": len(self.events), "at_ms": int((time.monotonic() - self.started) * 1000),
                 "kind": kind}
        event.update(fields)
        self.events.append(event)
        if self.path:
            with open(self.path, "a", encoding="utf-8") as handle:
                handle.write(json.dumps(event, ensure_ascii=False) + "\n")
        if not self.quiet:
            detail = " ".join("%s=%s" % (key, value) for key, value in fields.items()
                              if value not in ("", None, [], {}))
            sys.stderr.write("[%6d ms] %-10s %s\n" % (event["at_ms"], kind, detail))
        return event

MEMORA = os.environ.get("MEMORA_CLI", "memora")
JEV = os.environ.get(
    "JEV_SELECT", os.path.join(os.path.dirname(os.path.abspath(__file__)), "jev_select.py"))

AUTHORIZATION_VERSION = "memora.authorization/v2"
RECORDING_VERSION = "memora.jev-tree-recording/v1"

# One budget, and it bounds something outside the tree: a hosted provider that
# hangs, retries or black-holes. The tree's own size is deliberately not bounded
# here. A layer's width is the write path's business — `route_policy.
# branch_fanout` refuses a new child past the limit, and the read side says so
# itself ("a route layer is bounded by the Database's route_policy.branch_fanout,
# not by a read-side budget") — the tree is finite, and an `empty` from jev prunes
# a branch outright. Capping calls, width or depth here was the read side guessing
# the shape of the write side, and guessing wrong dropped branches by arrival
# order, which is how a whole Table's root layer could disappear from an answer.
# See docs/planning/whole-layer-read.md.
MAX_SECONDS = 120.0
# Flattening the Table layer into one request is worth it for a handful of
# databases. Past that the option set stops being a question anything can answer,
# and the walk asks each database in turn instead.
MAX_FLAT_DATABASES = 3
MAX_FLAT_OPTIONS = 25

EXIT_ANSWERED = 0
EXIT_NOT_CONFIGURED = 2
EXIT_PROVIDER_REFUSED = 3
EXIT_BAD_INPUT = 4


def fail(code, message):
    json.dump({"error": message}, sys.stdout, ensure_ascii=False)
    sys.stdout.write("\n")
    sys.exit(code)


class Budget:
    def __init__(self):
        self.jev_calls = 0
        self.started = time.monotonic()

    def exhausted(self):
        # The only gate: the provider's own uncertainty. `jev_calls` is still
        # counted and reported, because an unbounded walk against a metered API
        # needs observing — but counting is not capping.
        if time.monotonic() - self.started > MAX_SECONDS:
            return "budget: wall clock"
        return ""


class Engine:
    """The read faces this path needs, optionally served from a recording."""

    def __init__(self, recorder=None, recorded=None, log=None):
        self.recorder = recorder
        self.recorded = recorded
        self.log = log
        self.statements = []
        self.spent_ms = 0

    @staticmethod
    def key(source, databases, named):
        # The scope is part of the key, not decoration: the same statement text
        # answers differently per authorized Database (`SHOW CATALOG ATLAS` is the
        # obvious one), and a recording that keyed on the text alone would let one
        # database's answer stand in for another's.
        return json.dumps({"source": source, "databases": list(databases),
                           "named": named or {}}, ensure_ascii=False, sort_keys=True)

    def query(self, source, databases, named=None, label=""):
        key = self.key(source, databases, named or {})
        self.statements.append({"source": source, "databases": databases, "named": named or {}})
        started = time.monotonic()
        if self.recorded is not None:
            if key not in self.recorded:
                fail(EXIT_BAD_INPUT, "the recording has no engine answer for %s" % key)
            answer = self.recorded[key]
            self.note(source, databases, answer, label, started, recorded=True)
            return answer
        payload = {"authorization": {
            "version": AUTHORIZATION_VERSION, "actor": "agent:host",
            "authorized_databases": databases, "default_level": "L0"}}
        if named:
            payload["parameters"] = {"named": named}
        completed = subprocess.run(
            [MEMORA, "query", "--input", json.dumps(payload, ensure_ascii=False), source],
            capture_output=True, text=True)
        if completed.returncode != 0:
            fail(EXIT_PROVIDER_REFUSED, completed.stderr.strip() or completed.stdout.strip())
        envelope = json.loads(completed.stdout)
        statement = envelope["results"][0]
        if statement["status"] != "succeeded":
            fail(EXIT_PROVIDER_REFUSED, "%s: %s" % (
                statement["error"]["code"], statement["error"]["message"]))
        answer = {"rows": statement["rows"]}
        if self.recorder is not None:
            # Every answer this run stood on, so the same walk can be replayed
            # without a database: that is what makes this path testable in CI.
            self.recorder[key] = answer
        self.note(source, databases, answer, label, started)
        return answer

    def note(self, source, databases, answer, label, started, recorded=False):
        spent = int((time.monotonic() - started) * 1000)
        self.spent_ms += spent
        if self.log:
            self.log.emit("statement", layer=label, source=source, databases=list(databases),
                          rows=len(answer.get("rows", [])), duration_ms=spent,
                          recorded=recorded)


class Chooser:
    """One layer's decision, made by the shipped jev script.

    No probability crosses this boundary: `jev_select.py` decides the set and
    hands back names plus `separated`/`undecided`/`empty`.
    """

    def __init__(self, intent, budget, recorder=None, recorded=None, log=None, provider=None):
        self.intent = intent
        self.budget = budget
        self.recorder = recorder
        self.recorded = recorded
        self.log = log
        self.provider = provider
        self.model = ""
        self.spent_ms = 0
        # Decisions taken, live or replayed; `budget.jev_calls` counts provider
        # calls, which is what a replay does not make.
        self.decisions = 0

    def decide(self, options, layer="", undescribed=None):
        # The key is the intent plus the option names in the order they were
        # offered. Not sorted: the caller's order is deterministic, and sorting
        # would make the key depend on collation rather than on the question.
        key = self.intent + " || " + "|".join(name for name, _ in options)
        started = time.monotonic()
        if self.recorded is not None:
            if key not in self.recorded:
                fail(EXIT_BAD_INPUT, "the recording has no jev answer for %s" % key)
            answer = self.recorded[key]
        else:
            self.budget.jev_calls += 1
            criteria = {name: purpose for name, purpose in options}
            if self.provider is not None:
                answer = self.ask_in_process(criteria)
            else:
                request = {"mode": "set", "intent": self.intent,
                           "options": [{"name": name, "purpose": purpose} for name, purpose in options]}
                completed = subprocess.run(
                    [sys.executable, JEV], input=json.dumps(request, ensure_ascii=False),
                    capture_output=True, text=True)
                try:
                    answer = json.loads(completed.stdout)
                except json.JSONDecodeError:
                    fail(EXIT_PROVIDER_REFUSED, "jev answered nothing readable: %s"
                         % completed.stdout[:200])
                if completed.returncode != 0:
                    code = EXIT_NOT_CONFIGURED if completed.returncode == 2 else EXIT_PROVIDER_REFUSED
                    fail(code, answer.get("error", "jev exited %d" % completed.returncode))
        self.decisions += 1
        self.model = answer.get("model", self.model)
        if self.recorder is not None:
            self.recorder[key] = answer
        spent = int((time.monotonic() - started) * 1000)
        self.spent_ms += spent
        if self.log:
            self.log.emit("decision", layer=layer, options=len(options),
                          option_names=[name for name, _ in options],
                          undescribed=list(undescribed or []),
                          chosen=answer["relevant"], decision=answer["decision"],
                          provider_ms=answer.get("elapsed_ms"), duration_ms=spent,
                          recorded=self.recorded is not None)
        return answer["relevant"], answer["decision"]

    def ask_in_process(self, criteria):
        """The set question, asked on the walk's own connection.

        The cut, the floor probe and the sentinel live in `jev_select`; this only
        supplies the connection and turns its answer into the same shape the
        subprocess path returns, so both paths decide identically.
        """
        payload = jev_select.build_set_payload(self.intent, criteria, self.provider.model)
        started = time.monotonic()
        raw, error = self.provider.send(payload)
        elapsed_ms = int((time.monotonic() - started) * 1000)
        if error:
            fail(EXIT_PROVIDER_REFUSED, "%s (after %d ms)" % (error, elapsed_ms))
        answers = raw.get("answers", {})
        values = {}
        for index, name in enumerate(criteria):
            value = answers.get("holds_%d" % index, {}).get("noul")
            if not isinstance(value, (int, float)):
                fail(EXIT_PROVIDER_REFUSED, "the provider did not answer for %r" % name)
            values[name] = float(value)
        floor = answers.get(jev_select.SENTINEL, {}).get("noul")
        if not isinstance(floor, (int, float)):
            fail(EXIT_PROVIDER_REFUSED, "the provider did not answer the floor probe")
        relevant, decision = jev_select.decide_set(values, float(floor))
        return {"mode": "set", "relevant": relevant, "decision": decision,
                "model": raw.get("model"), "elapsed_ms": elapsed_ms}


def fold(text):
    """One comparable form for a label: full/half width and compatibility forms
    folded together, case folded, and the padding gone. Compared raw, a space or
    a full-width letter would be enough to pass a repeat off as a description.
    """
    return " ".join(unicodedata.normalize("NFKC", text or "").split()).casefold()


def described(name, purpose):
    """Whether this candidate carries a description at all.

    A `purpose` that is blank, or that only repeats the `name`, describes
    nothing — the two are the same absence, and the walk used to erase the
    difference by handing jev the name in the purpose's place. See
    docs/decisions.md「语义树的标签质量是可测量的检索损伤」.
    """
    folded = fold(purpose)
    return bool(folded) and folded != fold(name)


def choose_layer(chooser, budget, options, layer, evidence):
    """One layer: nothing to decide for a single child, jev otherwise.

    A layer the model answered without separating is **enumerated**, per the rule
    the Skill states: dropping it would silently lose everything behind it, and
    the model did not make a filter worth trusting. `empty` (the floor probe won)
    means nothing here answers the intent, which is a result, not a failure.

    A candidate that carries no description is offered with none, and the layer
    says so: the name is what the thing is already called, so repeating it in
    the purpose's place is how a layer nobody described came to look exactly
    like a layer somebody did.
    """
    if not options:
        return [], "empty"
    offered, undescribed = [], []
    for name, purpose, aliases in options:
        parts = []
        if described(name, purpose):
            parts.append(purpose)
        if aliases:
            # The owner's own words for this place, offered beside the description
            # rather than instead of it. Short terms, not the synopsis: the long
            # description stays on-demand by design, and a thousand characters per
            # sibling is noise where a phrase is signal (docs/decisions.md).
            parts.append("、".join(aliases))
        text = " ／ ".join(parts)
        if not text:
            undescribed.append(name)
        offered.append((name, text))
    options = offered
    if len(options) == 1:
        if chooser.log:
            chooser.log.emit("skipped", layer=layer, reason="single child", options=1,
                             chosen=[options[0][0]])
        return [options[0][0]], "single"
    stop = budget.exhausted()
    if stop:
        if chooser.log:
            chooser.log.emit("skipped", layer=layer, reason=stop, options=len(options))
        return [], stop
    started = time.monotonic()
    relevant, decision = chooser.decide(options, layer, undescribed)
    if decision == "undecided":
        relevant = [name for name, _ in options]
    entry = {
        "layer": layer, "mode": "set",
        # The option text the decision was made from travels with it: an answer
        # that cannot be audited later is a guess with a receipt.
        "options": [{"name": name, "purpose": purpose} for name, purpose in options],
        "relevant": relevant, "decision": decision,
        "options_count": len(options), "elapsed_ms": int((time.monotonic() - started) * 1000),
    }
    if undescribed:
        # Said in the answer, not swallowed: this layer was chosen from bare
        # names, so whatever it decided, it decided blind.
        entry["undescribed"] = undescribed
    evidence.append(entry)
    return relevant, decision


def table_options(engine, database, requirement):
    # The Atlas is scoped to one Database. Asking with several in the envelope
    # returns every Table of all of them, which is how a Table gets attributed to
    # the wrong library.
    atlas = engine.query("SHOW CATALOG ATLAS LIMIT :limit BYTES :bytes COMPACT", [database],
                         named={"limit": 64, "bytes": 8192}, label="tables of " + database)
    return [(row["table"], row.get("purpose", ""), row.get("aliases") or []) for row in atlas["rows"]
            if row.get("kind") == "table"]


def select_tables(engine, chooser, budget, selected_databases, requirement, evidence):
    """Which Table, inside each selected Database (or across them if few)."""
    if len(selected_databases) == 1:
        database = selected_databases[0]
        options = table_options(engine, database, requirement)
        chosen, decision = choose_layer(chooser, budget, options, "tables of " + database, evidence)
        return [(database, table) for table in chosen]

    # Several databases answered, which is a legal outcome for a requirement that
    # really points at more than one. Flatten their Tables into one question —
    # the option carries the database it came from — but only while that question
    # stays answerable; past the cap, ask each database in turn.
    flattened = []
    for database in selected_databases:
        for table, purpose, aliases in table_options(engine, database, requirement):
            flattened.append(("%s.%s" % (database, table), purpose, aliases))
    if len(flattened) <= MAX_FLAT_OPTIONS:
        chosen, decision = choose_layer(chooser, budget, flattened, "tables of " + ", ".join(selected_databases), evidence)
        tables = []
        for name in chosen:
            database, _, table = name.partition(".")
            tables.append((database, table))
        return tables
    tables = []
    for database in selected_databases:
        options = table_options(engine, database, requirement)
        chosen, decision = choose_layer(chooser, budget, options, "tables of " + database, evidence)
        tables.extend((database, table) for table in chosen)
    return tables


def read_plan(engine, landings):
    """The landings grouped into the reads they imply.

    The walk returns positions; reading the facts is the agent's job. Handing the
    same set back grouped by (database, table), with the table's column names, is
    the difference between one call per table and one call per landing: the widest
    measured requirement landed 35 rows across 6 tables, which is 6 calls against
    35. Each call carries one statement per landing — `SELECT … WHERE row_id =
    :row LIMIT 1`, one `--input` element each — because MSQL's read surface has no
    `IN` on `row_id`, and every `SELECT` carries a `LIMIT`.

    The columns cost one `DESCRIBE TABLE` per table that actually has landings —
    never per landing — and they are reported rather than assumed, because the
    engine owns the row's shape (ADR-0014) and this script does not.
    """
    groups, order = {}, []
    for landing in landings:
        key = (landing["database"], landing["table"])
        if key not in groups:
            groups[key] = []
            order.append(key)
        groups[key].append(landing)
    plan = []
    for database, table in order:
        described = engine.query("DESCRIBE TABLE %s.%s" % (database, table), [database],
                                 label="columns of %s.%s" % (database, table))
        columns = []
        if described["rows"]:
            columns = [column.get("name", "") for column in described["rows"][0].get("columns") or []]
        plan.append({
            "database": database, "table": table, "columns": columns,
            "rows": [{"path": row["path"], "row_id": row["row_id"], "revision": row["revision"]}
                     for row in groups[(database, table)]],
        })
    return plan


def incomplete_message(uncertain, stopped):
    """What a caller should do about an answer that is not whole.

    Two different reasons, said plainly, because they need two different actions:
    a layer judged from names alone is thin evidence, and a branch the walk never
    reached is missing evidence. Widening a budget is not one of the actions — the
    tree's size is the write path's business — so the advice is to narrow the
    question or name the Table.
    """
    if not uncertain and not stopped:
        return ""
    parts = []
    if stopped:
        parts.append("%d branch(es) were never reached (%s)" % (
            len(stopped), stopped[0].get("reason", "unknown")))
    if uncertain:
        parts.append("%d layer(s) were judged from names alone" % len(uncertain))
    return ("this answer is not whole: %s. Narrow it — name the Table, or ask one "
            "topic per call — rather than reading the landings as complete." % "; ".join(parts))


def walk(engine, chooser, budget, tables, requirement, evidence):
    """The semantic tree, breadth first.

    Breadth first because a budget that runs out should leave every branch at the
    same depth: a depth-first run that stops early answers one branch and never
    touches another, which is a worse answer than a shallow one across all of
    them.
    """
    landings = []
    stopped = []
    frontier = []
    damage = []
    visited = set()
    for database, table in tables:
        rows = engine.query("SHOW ROUTES FROM TABLE %s.%s AT ROOT" % (database, table), [database],
                            label="%s.%s root" % (database, table))["rows"]
        if not rows:
            evidence.append({"layer": "%s.%s" % (database, table), "decision": "no_root",
                             "options": [], "relevant": []})
            continue
        frontier.append({"database": database, "table": table,
                         "parent": rows[0]["parent_id"], "path": []})

    while frontier:
        advanced = []
        for node in frontier:
            label = "/".join(segment["name"] for segment in node["path"]) or "root"
            if node["parent"] in visited:
                # A Route reached twice means the tree has a cycle, which the write
                # path refuses to build (ROUTE MUTATION checks descent) — so this is
                # damaged data, and doctor's business. Skipping it here keeps the
                # walk finite; reporting it keeps it from being silent. A depth cap
                # used to stand in for this and pretended the tree was too deep.
                damage.append({"database": node["database"], "table": node["table"],
                               "route_id": node["parent"],
                               "path": node["database"] + ":" + label})
                continue
            visited.add(node["parent"])
            children = engine.query("SHOW ROUTES UNDER :parent", [node["database"]],
                                    named={"parent": node["parent"]}, label=label)["rows"]
            options = [(row["name"], row.get("purpose", ""), row.get("aliases") or [])
                       for row in children]
            # The Database is part of the label: a bare "root" names one layer in
            # each Database, and `incomplete_at` has to say which one it means.
            layer = node["database"] + ":" + label
            chosen, decision = choose_layer(chooser, budget, options, layer, evidence)
            if decision.startswith("budget:"):
                # Not a landing. The branch was never looked at, and saying it was
                # "reached" is how an answer that could not reach the fact came to
                # look exactly like one that searched and found nothing.
                stopped.append({"layer": layer, "reason": decision, "candidates": len(children)})
                continue
            for row in children:
                if row["name"] not in chosen:
                    continue
                path = node["path"] + [{"name": row["name"], "route_id": row["route_id"]}]
                child = "/" + "/".join(segment["name"] for segment in path)
                if row["kind"] == "leaf":
                    # The Row comes from the layer listing, not from one
                    # `OPEN ROUTE :leaf LIMIT 1` per leaf. `SHOW ROUTES` carries
                    # `row_id`/`row_revision` on every leaf because the write path
                    # guarantees a live Row hangs under exactly one leaf, so the
                    # listing already knows what opening the leaf would say. The
                    # widest measured walk spent 35 of its 50 statements asking
                    # that question one leaf at a time.
                    #
                    # `row_revision` is the Row's version, and it is what this
                    # answer's `revision` has always meant — the node's own
                    # `revision` is a different counter on a different object and
                    # must not be handed to a caller writing a Row.
                    landings.append({
                        # The Table is what a back-table read needs: a path is unique
                        # within a Table, and a row id alone cannot be written into
                        # `SELECT … FROM <db>.<table> WHERE row_id = :row`.
                        "database": node["database"], "table": node["table"],
                        "path": child, "leaf_route_id": row["route_id"],
                        "row_id": row.get("row_id"),
                        "revision": row.get("row_revision"),
                        "termination": "leaf",
                    })
                    continue
                advanced.append({"database": node["database"], "table": node["table"],
                                 "parent": row["route_id"], "path": path})
        # No truncation: everything the layer produced goes on to be looked at.
        frontier = advanced
    return landings, stopped, damage


def main():
    parser = argparse.ArgumentParser(description="Walk to the positions a requirement points at, with jev")
    parser.add_argument("--record", metavar="FILE", default=None,
                        help="write every engine and jev answer to FILE")
    parser.add_argument("--replay", metavar="FILE", default=None,
                        help="run from a recording: no database, no provider")
    parser.add_argument("--log", metavar="FILE", default=None,
                        help="also write the step log to FILE as JSON lines")
    parser.add_argument("--quiet", action="store_true",
                        help="do not print the step log to stderr")
    arguments = parser.parse_args()
    if arguments.log and os.path.exists(arguments.log):
        os.remove(arguments.log)
    log = StepLog(arguments.log, arguments.quiet)

    recording = {"version": RECORDING_VERSION, "request": None, "engine": {}, "jev": {}}
    if arguments.replay:
        try:
            with open(arguments.replay, encoding="utf-8") as handle:
                recording = json.load(handle)
        except (OSError, json.JSONDecodeError) as error:
            fail(EXIT_BAD_INPUT, "replay file is not readable JSON: %s" % error)
        if recording.get("version") != RECORDING_VERSION:
            fail(EXIT_BAD_INPUT, "replay file is not a %s recording" % RECORDING_VERSION)
        request = recording["request"]
    else:
        try:
            request = json.load(sys.stdin)
        except json.JSONDecodeError as error:
            fail(EXIT_BAD_INPUT, "the request must be JSON on stdin: %s" % error)
        recording["request"] = request

    requirement = request.get("requirement", "")
    databases = request.get("authorized_databases", [])
    if not isinstance(requirement, str) or not requirement.strip():
        fail(EXIT_BAD_INPUT, "the request needs a non-empty requirement")
    if (not isinstance(databases, list) or not databases
            or not all(isinstance(name, str) and name.strip() for name in databases)):
        fail(EXIT_BAD_INPUT, "the request needs a non-empty authorized_databases list")

    budget = Budget()
    log.emit("start", requirement=requirement, authorized_databases=list(databases),
             mode="replay" if arguments.replay else "live")
    engine = Engine(recorder=recording["engine"] if arguments.record else None,
                    recorded=recording["engine"] if arguments.replay else None, log=log)
    # One provider connection for the whole walk when it is live; a replay asks
    # nobody and needs none. `JEV_IN_PROCESS=0` falls back to one subprocess per
    # decision, which is slower but keeps a broken import from breaking the walk.
    provider = None
    if not arguments.replay and os.environ.get("JEV_IN_PROCESS", "1") != "0":
        provider = jev_select.Provider()
        if not provider.api_key:
            fail(EXIT_NOT_CONFIGURED,
                 "TYPESAFE_API_KEY is not set; choose the layer yourself or ask the user")
        log.emit("provider", base_url=provider.base_url, model=provider.model, mode="reused connection")
    chooser = Chooser(requirement, budget,
                      recorder=recording["jev"] if arguments.record else None,
                      recorded=recording["jev"] if arguments.replay else None, log=log,
                      provider=provider)
    evidence, stopped_early = [], ""

    # Level 0 — which Database. Only the authorized ones are ever offered: a
    # name outside that scope must never reach the model, not even as a negative.
    catalogue = engine.query("SHOW DATABASES", databases, label="databases")
    offered = [row for row in catalogue["rows"] if row["name"] in databases]
    options = [(row["name"], row.get("purpose", ""), row.get("aliases") or []) for row in offered]
    if request.get("database"):
        selected = [request["database"]]
        evidence.append({"layer": "databases", "mode": "named", "options": [],
                         "relevant": selected, "decision": "named"})
    else:
        selected, decision = choose_layer(chooser, budget, options, "databases", evidence)
        if not selected:
            stopped_early = "no database matched" if decision == "empty" else decision

    tables = []
    if selected and not stopped_early:
        if request.get("table"):
            tables = [(database, request["table"]) for database in selected]
            evidence.append({"layer": "tables", "mode": "named", "options": [],
                             "relevant": [request["table"]], "decision": "named"})
        else:
            tables = select_tables(engine, chooser, budget, selected, requirement, evidence)
        if not tables:
            stopped_early = "no table matched"

    landings, walked_stops, damage = (walk(engine, chooser, budget, tables, requirement, evidence)
                                      if tables else ([], [], []))
    uncertain = [entry["layer"] for entry in evidence if entry.get("decision") == "undecided"]
    # Layers whose candidates arrived with no description. Not a failure and not
    # incompleteness — the walk still decided — but the decision was made from
    # names alone, and a caller who cannot see that has no way to know why the
    # answer is thin. Filling those purposes is the repair; this is the meter.
    undescribed_at = [entry["layer"] for entry in evidence if entry.get("undescribed")]
    # Layers jev pruned by answering "nothing here". That is a result, not a fault,
    # and it is also why a branch can vanish from an answer: without this line the
    # pruning would be visible only to a caller that reads the whole evidence list.
    pruned_at = [entry["layer"] for entry in evidence if entry.get("decision") == "empty"]
    stopped = walked_stops
    if stopped_early:
        # Nothing was walked at all — the Database or Table layer found no match.
        stopped = [{"layer": "selection", "reason": stopped_early, "candidates": 0}]
    result = {
        "requirement": requirement,
        "authorized_databases": databases,
        "landings": landings,
        # `incomplete` now covers both reasons: a layer judged from names alone,
        # and a branch the walk never reached. Neither is a partial answer to be
        # used as a whole one.
        "incomplete": bool(uncertain or stopped),
        "incomplete_at": uncertain,
        "undescribed_at": undescribed_at,
        "pruned_at": pruned_at,
        "evidence": evidence,
        "jev_calls": budget.jev_calls,
        "decisions": chooser.decisions,
        "statements": len(engine.statements),
        "timings": {"engine_ms": engine.spent_ms, "jev_ms": chooser.spent_ms},
        "elapsed_ms": int((time.monotonic() - budget.started) * 1000),
        "model": chooser.model,
    }
    if stopped:
        result["stopped"] = stopped
    if landings:
        # The same set, grouped into the reads it implies, so the agent writes one
        # statement per table instead of one per landing.
        result["reads"] = read_plan(engine, landings)
    if damage:
        # A Route reached twice: the tree is damaged, and doctor is where that gets
        # repaired. Reported rather than swallowed, and no longer disguised as a
        # depth budget running out.
        result["tree_damage"] = damage
    if incomplete_message(uncertain, stopped):
        result["suggest"] = incomplete_message(uncertain, stopped)
    log.emit("done", landings=len(landings), decisions=chooser.decisions,
             statements=len(engine.statements), engine_ms=engine.spent_ms,
             jev_ms=chooser.spent_ms, incomplete=bool(uncertain or stopped),
             undescribed_at=undescribed_at, pruned_at=pruned_at, stopped=len(stopped))
    if provider is not None:
        provider.close()
    if arguments.record:
        with open(arguments.record, "w", encoding="utf-8") as handle:
            json.dump(recording, handle, ensure_ascii=False, indent=1)
            handle.write("\n")
    json.dump(result, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return EXIT_ANSWERED


if __name__ == "__main__":
    sys.exit(main())
