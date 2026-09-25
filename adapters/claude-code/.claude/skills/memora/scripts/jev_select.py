#!/usr/bin/env python3
"""Decide which child of one layer to descend into, using jev.

This is the fourth retrieval path, and it lives entirely on the Skill side: the
kernel still answers `SHOW ROUTES`, and this script only decides which of those
answers to follow. Nothing about it changes the engine, and the engine never
learns that jev exists.

Two modes, because a layer answers one of two questions. Which one it is comes
from the layer's structure, not from guessing the user's intent:

* `"mode": "choice"` (the default) — the children are **alternatives**, only one
  of which can be right ("which Table holds this?"). One `Choice` question.
* `"mode": "set"` — the children are **instances of one kind** and several may
  apply ("which of my internships?"). One `Noul` question per child, all in one
  request, so the answer is a set. A `Choice` here can only ever return one, and
  asking it to return two is how a second internship goes missing.

Contract
--------
    stdin : {"intent": "<what the user wants>",
             "mode": "choice" | "set",
             "options": [{"name": "<child name>", "purpose": "<child purpose>"}, ...]}
    stdout choice: {"mode": "choice", "choice": "<name>", "confidence": 0.0-1.0,
             "probabilities": {"<name>": 0.0-1.0, ...}, "model": "<model>",
             "elapsed_ms": <the provider round trip, integer>}
    stdout set   : {"mode": "set", "relevant": ["<name>", ...],
             "decision": "separated" | "undecided" | "empty",
             "model": "<model>", "elapsed_ms": <integer>}
    exit  : 0 answered | 2 not configured | 3 provider refused | 4 bad input
            | 5 below --min-confidence

In `set` mode no probability leaves this process. The cut is decided here, by the
rule in `decide_set`, and the caller receives names and a decision — never a
score it could threshold, rank or store. `separated` means the request's own
answers split cleanly; `undecided` means they did not, so the caller enumerates
the layer instead of trusting a filter; `empty` means nothing stood out at all,
which is the same signal as the `none` option in `choice` mode.

`model` is the model that **served** the answer, not the alias that was asked for.
`elapsed_ms` is measured around the provider call, because "a jev decision is
worth a second" is a claim a caller has to be able to check: the round trip is a
fresh process and a fresh TLS connection to a hosted API, and it dominates the
model's own time.

Route IDs are never part of the request. The caller hands over names and purposes
only, so an identifier that is not an authorization token cannot end up in a
model prompt — the security rule the retrieval design already wrote down. A
purpose the caller did not write is sent as empty and never as the name: the name
is already the candidate, so standing it in the purpose's place adds nothing and
turns "nobody described this layer" into "somebody did".

State of the art, not gospel: `confidence` summarizes how concentrated jev's
distribution was. It is not permission to act, and it says nothing about whether
the layer even contains the right child. A caller should treat a low value as
"look further, or decide yourself and say what you decided", never as a
probability that the answer is correct in the world.
"""

import argparse
import http.client
import json
import os
import pathlib
import re
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_BASE_URL = "https://api.typesafe.ai"
DEFAULT_MODEL = "jev-latest"
REQUEST_TIMEOUT_SECONDS = 30
RETRY_DELAYS_SECONDS = (0.5, 1.5)

MODE_CHOICE = "choice"
MODE_SET = "set"

# The sentinel is the whole reason `set` mode needs no magic relevance number: it
# is one extra Noul asking whether this layer holds nothing for the intent, and it
# travels in the same request. "Is 0.63 relevant?" becomes "is 0.63 separated from
# what this very request called nothing?" — a comparison inside one request, not a
# constant calibrated per library, per user, or per corpus.
SENTINEL = "nothing_here"
SENTINEL_QUESTION = ("Is it true that none of the places in `candidates` holds any "
                     "part of what `state.intent` asks about?")

# How far apart two answers must be before the request is called decisive. It is
# fixed and deliberately not configurable: it measures whether the model answered
# decisively this time, not how relevant anything is, and a tunable version would
# be exactly the relevance threshold this design refuses to have.
SEPARATION_FACTOR = 2.0

EXIT_ANSWERED = 0
EXIT_NOT_CONFIGURED = 2
EXIT_PROVIDER_REFUSED = 3
EXIT_BAD_INPUT = 4
EXIT_BELOW_THRESHOLD = 5


def from_environment(name):
    """Read a setting from the environment, falling back to the user's profile.

    An agent often runs with a non-interactive environment, where the shell
    profile has not been sourced; scanning the profile for the assignment keeps
    the documented setup working without executing anyone's rc file. Values are
    never printed.
    """
    value = os.environ.get(name, "")
    if value:
        return value
    profile = pathlib.Path(os.environ.get("TYPESAFE_ENV_FILE", os.path.expanduser("~/.zshrc")))
    if not profile.exists():
        return ""
    found = re.findall(r"^\s*(?:export\s+)?" + re.escape(name) + r"=(.*)$", profile.read_text(), re.M)
    return found[-1].strip().strip('"').strip("'") if found else ""


def fail(code, message):
    json.dump({"error": message}, sys.stdout)
    sys.stdout.write("\n")
    sys.exit(code)


def read_request():
    try:
        request = json.load(sys.stdin)
    except json.JSONDecodeError as error:
        fail(EXIT_BAD_INPUT, "the request must be JSON on stdin: %s" % error)
    intent = request.get("intent", "")
    options = request.get("options", [])
    mode = request.get("mode", MODE_CHOICE)
    if not isinstance(intent, str) or not intent.strip():
        fail(EXIT_BAD_INPUT, "the request needs a non-empty intent")
    if mode not in (MODE_CHOICE, MODE_SET):
        fail(EXIT_BAD_INPUT, "mode must be %r or %r" % (MODE_CHOICE, MODE_SET))
    if not isinstance(options, list) or len(options) < 2:
        fail(EXIT_BAD_INPUT, "choosing needs at least two options")
    criteria = {}
    for option in options:
        name = option.get("name", "")
        if not isinstance(name, str) or not name.strip():
            fail(EXIT_BAD_INPUT, "every option needs a name")
        if name in criteria:
            fail(EXIT_BAD_INPUT, "option names must be unique: %s" % name)
        # Only the human-readable description travels; any other key the caller
        # passed (route_id, for instance) is dropped here rather than trusted.
        #
        # A missing purpose stays missing. Substituting the name here is how a
        # layer nobody described came to look exactly like a layer somebody did:
        # the name is already in the candidate, so putting it in the purpose's
        # place adds no information and hides the absence. The caller that owns
        # the labels reports them (see `undescribed_at` in jev_tree.py), and the
        # provider accepts an empty purpose — measured, it answers cleanly when
        # the other candidates carry real ones.
        criteria[name] = option.get("purpose") or ""
    if SENTINEL in criteria:
        fail(EXIT_BAD_INPUT, "option name %r is reserved for the floor probe" % SENTINEL)
    return intent, criteria, mode


def build_choice_payload(intent, criteria, model):
    return {
        "state": {"intent": intent},
        "model": model,
        "questions": {
            "child": {
                "type": "choice",
                "instructions": {
                    "question": "Which of these places is the one to look in for `intent`?",
                    "places": criteria,
                },
                "criteria": criteria,
            }
        },
    }


def build_set_payload(intent, criteria, model):
    # One Noul per candidate, plus the floor probe, in a single request. Noul is
    # the primitive TypeSafe documents for "several may apply": one question per
    # label, judged independently and in parallel, so the answer is a set rather
    # than a pick. The floor probe rides along so the cut below can be a
    # comparison inside this request instead of a number calibrated somewhere.
    candidates = [{"name": name, "purpose": purpose} for name, purpose in criteria.items()]
    questions = {}
    for index, (name, purpose) in enumerate(criteria.items()):
        questions["holds_%d" % index] = {
            "type": "noul",
            "instructions": {
                "candidate": {"name": name, "purpose": purpose},
                "question": "Is `candidate` one of the places the intent's answer lives in?",
            },
            "criteria": {
                "true": "this place holds part of, or all of, what the intent asks about",
                "false": "this place holds nothing the intent asks about",
            },
        }
    questions[SENTINEL] = {
        "type": "noul",
        "instructions": {"question": SENTINEL_QUESTION},
        "criteria": {
            "true": "none of the candidates holds any part of what the intent asks about",
            "false": "at least one candidate holds part of what the intent asks about",
        },
    }
    return {
        "state": {"intent": intent, "candidates": candidates},
        "model": model,
        "questions": questions,
    }


def decide_set(ranked, floor):
    # Turn per-candidate Noul values plus the floor probe into (names, decision).
    # The rule the Skill states, implemented once:
    #   1. rank the candidates and the floor probe together, as one ladder;
    #   2. cut at the largest ratio drop, and only if that drop is at least
    #      SEPARATION_FACTOR -- a clear gap in this request, not a relevance score;
    #   3. everything above the cut is the set. The floor probe winning the top
    #      means `empty` (nothing here answers the intent); no clear drop at all
    #      means `undecided`, and the caller enumerates the layer rather than
    #      trusting a filter the model did not make.
    ladder = sorted(list(ranked.items()) + [(SENTINEL, floor)],
                    key=lambda item: (-item[1], item[0]))
    best_index, best_ratio = None, 0.0
    for index in range(len(ladder) - 1):
        above = ladder[index][1]
        below = ladder[index + 1][1]
        if above <= 0:
            continue
        ratio = float("inf") if below <= 0 else above / below
        if ratio > best_ratio:
            best_ratio, best_index = ratio, index
    if best_index is None or best_ratio < SEPARATION_FACTOR:
        return [], "undecided"
    kept = sorted(name for name, _ in ladder[: best_index + 1] if name != SENTINEL)
    if not kept:
        return [], "empty"
    return kept, "separated"


def call_provider(base_url, api_key, payload):
    body = json.dumps(payload).encode()
    endpoint = base_url.rstrip("/") + "/v1/systemone"
    attempts = len(RETRY_DELAYS_SECONDS) + 1
    for attempt in range(attempts):
        request = urllib.request.Request(
            endpoint, data=body,
            headers={"Content-Type": "application/json", "Authorization": "Bearer " + api_key})
        try:
            with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT_SECONDS) as response:
                return json.loads(response.read()), None
        except urllib.error.HTTPError as error:
            # The body is read and dropped: a provider that echoes the request on
            # failure would otherwise put the key in a log.
            error.read()
            if error.code in (429, 529) and attempt + 1 < attempts:
                time.sleep(RETRY_DELAYS_SECONDS[attempt])
                continue
            return None, "the provider answered %s %s" % (error.code, error.reason)
        except urllib.error.URLError as error:
            return None, "could not reach the provider: %s" % error.reason
    return None, "the provider kept refusing"


class Provider:
    """One connection, reused for every decision a walk asks for.

    A fresh connection measured 696-804 ms per call against 244-523 ms on a
    reused one: TLS handshake is most of the difference, and a walk makes one call
    per layer with more than one child, so keeping the connection turns several
    hundred milliseconds per layer into a fraction of that. The first call still
    pays the handshake.

    This is not a daemon. A connection idle for minutes is closed by any proxy in
    the path, so holding one between *runs* would buy little and cost a process to
    supervise; holding one across the decisions inside a run is where the time is.
    """

    def __init__(self, base_url=None, api_key=None, model=None):
        self.base_url = (base_url or from_environment("TYPESAFE_BASE_URL") or DEFAULT_BASE_URL).rstrip("/")
        self.api_key = api_key if api_key is not None else from_environment("TYPESAFE_API_KEY")
        self.model = model or from_environment("TYPESAFE_MODEL") or DEFAULT_MODEL
        self.connection = None

    def _connect(self):
        parts = urllib.parse.urlsplit(self.base_url)
        if parts.scheme == "https":
            return http.client.HTTPSConnection(parts.netloc, timeout=REQUEST_TIMEOUT_SECONDS,
                                               context=ssl.create_default_context())
        return http.client.HTTPConnection(parts.netloc, timeout=REQUEST_TIMEOUT_SECONDS)

    def send(self, payload):
        """One request on the kept connection, reconnecting once if it went away."""
        body = json.dumps(payload).encode()
        path = urllib.parse.urlsplit(self.base_url).path.rstrip("/") + "/v1/systemone"
        headers = {"Content-Type": "application/json", "Authorization": "Bearer " + self.api_key}
        attempts = len(RETRY_DELAYS_SECONDS) + 1
        for attempt in range(attempts):
            if self.connection is None:
                self.connection = self._connect()
            try:
                self.connection.request("POST", path, body=body, headers=headers)
                response = self.connection.getresponse()
                raw = response.read()
                if response.status < 200 or response.status > 299:
                    # The body is read and dropped: a provider that echoes the
                    # request on failure would otherwise put the key in a log.
                    if response.status in (429, 529) and attempt + 1 < attempts:
                        time.sleep(RETRY_DELAYS_SECONDS[attempt])
                        continue
                    return None, "the provider answered %s %s" % (response.status, response.reason)
                return json.loads(raw), None
            except (http.client.HTTPException, OSError) as error:
                # A kept connection can be closed by the far end between calls.
                # Dropping it and trying once more is the whole cost of reusing it;
                # only a failure after a fresh connection is a provider failure.
                self.close()
                if attempt + 1 >= attempts:
                    return None, "could not reach the provider: %s" % error
        return None, "the provider kept refusing"

    def close(self):
        if self.connection is not None:
            try:
                self.connection.close()
            except OSError:
                pass
            self.connection = None


def replay(path):
    """Re-run the set-mode cut on recorded provider answers.

    The cut is the only part of this path that decides anything, so it is the
    part worth testing: a recorded answer file turns it into a deterministic
    function of numbers that came from a real request. No network, no key, and
    nothing printed but the same shape a live call returns.
    """
    try:
        with open(path, encoding="utf-8") as handle:
            recorded = json.load(handle)
    except (OSError, json.JSONDecodeError) as error:
        fail(EXIT_BAD_INPUT, "replay file is not readable JSON: %s" % error)
    names = recorded.get("options")
    answers = recorded.get("answers")
    if not isinstance(names, list) or len(names) < 2 or not isinstance(answers, dict):
        fail(EXIT_BAD_INPUT, "a replay file needs options and answers")
    values = {}
    for index, name in enumerate(names):
        value = answers.get("holds_%d" % index, {}).get("noul")
        if not isinstance(value, (int, float)):
            fail(EXIT_BAD_INPUT, "replay is missing an answer for %r" % name)
        values[name] = float(value)
    floor = answers.get(SENTINEL, {}).get("noul")
    if not isinstance(floor, (int, float)):
        fail(EXIT_BAD_INPUT, "replay is missing the floor probe")
    relevant, decision = decide_set(values, float(floor))
    json.dump({"mode": MODE_SET, "relevant": relevant, "decision": decision,
               "model": recorded.get("model", "replay"), "elapsed_ms": 0}, sys.stdout)
    sys.stdout.write("\n")
    return EXIT_ANSWERED


def main():
    parser = argparse.ArgumentParser(description="Choose a layer's child with jev")
    parser.add_argument("--dry-run", action="store_true",
                        help="print the request that would be sent, send nothing")
    parser.add_argument("--min-confidence", type=float, default=None,
                        help="refuse an answer below this confidence instead of returning it")
    parser.add_argument("--replay", metavar="FILE", default=None,
                        help="apply the set-mode cut to recorded answers: no network, no key")
    arguments = parser.parse_args()
    if arguments.replay:
        return replay(arguments.replay)

    intent, criteria, mode = read_request()
    model = from_environment("TYPESAFE_MODEL") or DEFAULT_MODEL
    payload = (build_set_payload(intent, criteria, model) if mode == MODE_SET
               else build_choice_payload(intent, criteria, model))

    if arguments.dry_run:
        # No key needed and no call made: this is how the request shape is
        # checked without a provider, including that no id travelled.
        json.dump({"request": payload, "endpoint": "/v1/systemone"}, sys.stdout)
        sys.stdout.write("\n")
        return EXIT_ANSWERED

    api_key = from_environment("TYPESAFE_API_KEY")
    if not api_key:
        fail(EXIT_NOT_CONFIGURED,
             "TYPESAFE_API_KEY is not set; choose the child yourself and say which one you chose")
    base_url = from_environment("TYPESAFE_BASE_URL") or DEFAULT_BASE_URL

    started = time.monotonic()
    provider = Provider(base_url=base_url, api_key=api_key, model=model)
    answer, error = provider.send(payload)
    elapsed_ms = int((time.monotonic() - started) * 1000)
    provider.close()
    if error:
        fail(EXIT_PROVIDER_REFUSED, "%s (after %d ms)" % (error, elapsed_ms))
    answers = answer.get("answers", {})
    if mode == MODE_SET:
        values = {}
        for index, name in enumerate(criteria):
            value = answers.get("holds_%d" % index, {}).get("noul")
            if not isinstance(value, (int, float)):
                fail(EXIT_PROVIDER_REFUSED, "the provider did not answer for %r" % name)
            values[name] = float(value)
        floor = answers.get(SENTINEL, {}).get("noul")
        if not isinstance(floor, (int, float)):
            fail(EXIT_PROVIDER_REFUSED, "the provider did not answer the floor probe")
        relevant, decision = decide_set(values, float(floor))
        json.dump({"mode": MODE_SET, "relevant": relevant, "decision": decision,
                   "model": answer.get("model"), "elapsed_ms": elapsed_ms}, sys.stdout)
        sys.stdout.write("\n")
        return EXIT_ANSWERED

    chosen = answers.get("child", {})
    choice = chosen.get("choice")
    if not choice or choice not in criteria:
        fail(EXIT_PROVIDER_REFUSED, "the provider answered with something that is not one of the options")
    confidence = chosen.get("confidence")
    # The fallback has to be a decision the caller can act on, not a number it has
    # to interpret: given a threshold, a weak answer is refused here, and the
    # caller chooses the child itself or asks the user.
    if arguments.min_confidence is not None and (confidence is None or confidence < arguments.min_confidence):
        json.dump({"error": "confidence %s is below the requested minimum %s"
                            % (confidence, arguments.min_confidence),
                   "choice": choice, "confidence": confidence,
                   "model": answer.get("model"), "elapsed_ms": elapsed_ms}, sys.stdout)
        sys.stdout.write("\n")
        return EXIT_BELOW_THRESHOLD
    json.dump({"mode": MODE_CHOICE, "choice": choice, "confidence": confidence,
               "probabilities": chosen.get("probabilities", {}),
               "model": answer.get("model"), "elapsed_ms": elapsed_ms},
              sys.stdout)
    sys.stdout.write("\n")
    return EXIT_ANSWERED


if __name__ == "__main__":
    sys.exit(main())
