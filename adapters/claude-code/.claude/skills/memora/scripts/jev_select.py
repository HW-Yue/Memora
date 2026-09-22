#!/usr/bin/env python3
"""Decide which child of one layer to descend into, using jev.

This is the fourth retrieval path, and it lives entirely on the Skill side: the
kernel still answers `SHOW ROUTES`, and this script only decides which of those
answers to follow. Nothing about it changes the engine, and the engine never
learns that jev exists.

Contract
--------
    stdin : {"intent": "<what the user wants>",
             "options": [{"name": "<child name>", "purpose": "<child purpose>"}, ...]}
    stdout: {"choice": "<name>", "confidence": 0.0-1.0,
             "probabilities": {"<name>": 0.0-1.0, ...}, "model": "<model>",
             "elapsed_ms": <the provider round trip, integer>}
    exit  : 0 answered | 2 not configured | 3 provider refused | 4 bad input
            | 5 below --min-confidence

`model` is the model that **served** the answer, not the alias that was asked for.
`elapsed_ms` is measured around the provider call, because "a jev decision is
worth a second" is a claim a caller has to be able to check: the round trip is a
fresh process and a fresh TLS connection to a hosted API, and it dominates the
model's own time.

Route IDs are never part of the request. The caller hands over names and purposes
only, so an identifier that is not an authorization token cannot end up in a
model prompt — the security rule the retrieval design already wrote down.

State of the art, not gospel: `confidence` summarizes how concentrated jev's
distribution was. It is not permission to act, and it says nothing about whether
the layer even contains the right child. A caller should treat a low value as
"ask the user or decide yourself", never as a probability that the answer is
correct in the world.
"""

import argparse
import json
import os
import pathlib
import re
import sys
import time
import urllib.error
import urllib.request

DEFAULT_BASE_URL = "https://api.typesafe.ai"
DEFAULT_MODEL = "jev-latest"
REQUEST_TIMEOUT_SECONDS = 30
RETRY_DELAYS_SECONDS = (0.5, 1.5)

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
    if not isinstance(intent, str) or not intent.strip():
        fail(EXIT_BAD_INPUT, "the request needs a non-empty intent")
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
        criteria[name] = option.get("purpose") or option.get("name")
    return intent, criteria


def build_payload(intent, criteria, model):
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


def main():
    parser = argparse.ArgumentParser(description="Choose a layer's child with jev")
    parser.add_argument("--dry-run", action="store_true",
                        help="print the request that would be sent, send nothing")
    parser.add_argument("--min-confidence", type=float, default=None,
                        help="refuse an answer below this confidence instead of returning it")
    arguments = parser.parse_args()

    intent, criteria = read_request()
    model = from_environment("TYPESAFE_MODEL") or DEFAULT_MODEL
    payload = build_payload(intent, criteria, model)

    if arguments.dry_run:
        # No key needed and no call made: this is how the request shape is
        # checked without a provider, including that no id travelled.
        json.dump({"request": payload, "endpoint": "/v1/systemone"}, sys.stdout)
        sys.stdout.write("\n")
        return EXIT_ANSWERED

    api_key = from_environment("TYPESAFE_API_KEY")
    if not api_key:
        fail(EXIT_NOT_CONFIGURED,
             "TYPESAFE_API_KEY is not set; choose the child yourself or ask the user")
    base_url = from_environment("TYPESAFE_BASE_URL") or DEFAULT_BASE_URL

    started = time.monotonic()
    answer, error = call_provider(base_url, api_key, payload)
    elapsed_ms = int((time.monotonic() - started) * 1000)
    if error:
        fail(EXIT_PROVIDER_REFUSED, "%s (after %d ms)" % (error, elapsed_ms))
    chosen = answer.get("answers", {}).get("child", {})
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
    json.dump({"choice": choice, "confidence": confidence,
               "probabilities": chosen.get("probabilities", {}),
               "model": answer.get("model"), "elapsed_ms": elapsed_ms},
              sys.stdout)
    sys.stdout.write("\n")
    return EXIT_ANSWERED


if __name__ == "__main__":
    sys.exit(main())
