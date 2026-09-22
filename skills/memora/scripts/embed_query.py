#!/usr/bin/env python3
"""Turn a query into the vector `RECALL ... NEAREST :v` takes.

This is the missing half of the fast path. The engine never calls a model — that
is not a limitation to work around, it is the boundary that keeps the Provider on
the host — so a host that wants the vector arm has to compute the query vector
itself. The Admin gateway has always done that in Go; a host following this Skill
had no way to, which meant "vector + keyword" silently degraded to keyword on the
very path the Skill describes. This script is that half, and it is deliberately
the same shape as `jev_select.py`: host-side, reads the host's own environment,
never lets the key near the engine, a log, or an argument list.

The output is the statement's parameter, nothing more:

    {"vector": "<base64>", "model": "text-embedding-v4", "dimensions": 1024,
     "elapsed_ms": 123}

`vector` is the form `recall` documents — raw URL-safe base64 of little-endian
float32, no padding — so it goes straight into
`RECALL FROM <db> MATCH :q NEAREST :v LIMIT :n`.

Contract
--------
    stdin : {"text": "<what the user asked about>"}
    stdout: {"vector": "<base64>", "model": "<model>", "dimensions": <int>,
             "elapsed_ms": <int>}
    exit  : 0 answered | 2 embeddings are off or not configured
            | 3 the provider refused | 4 bad input

Exit 2 is not an error to hide: with no provider there is no vector arm, the
recall you get back will say `arms: ["keyword"]`, and the answer has to say so
rather than call it a fused recall.

`--encode 0.5,-1,0` prints the vector for a known list, with no network and no
key: that is how the encoding is checked against the engine's own
`recall.EncodeVector`. `--dry-run` prints the request that would be sent.
"""

import argparse
import base64
import json
import math
import os
import pathlib
import re
import struct
import sys
import time
import urllib.error
import urllib.request

ENV_SWITCH = "MEMORA_EMBEDDING"
ENV_BASE_URL = "MEMORA_EMBEDDING_BASE_URL"
ENV_MODEL = "MEMORA_EMBEDDING_MODEL"
ENV_DIMENSIONS = "MEMORA_EMBEDDING_DIMENSIONS"
ENV_API_KEY = "MEMORA_EMBEDDING_API_KEY"
OFF = "off"

# The Admin gateway gives the provider five seconds before it drops to the
# keyword arm; a query embedding that takes longer than that has already cost
# more than walking the tree would have.
REQUEST_TIMEOUT_SECONDS = 5.0

EXIT_ANSWERED = 0
EXIT_NOT_CONFIGURED = 2
EXIT_PROVIDER_REFUSED = 3
EXIT_BAD_INPUT = 4


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
    profile = pathlib.Path(os.environ.get("MEMORA_ENV_FILE", os.path.expanduser("~/.zshrc")))
    if not profile.exists():
        return ""
    found = re.findall(r"^\s*(?:export\s+)?" + re.escape(name) + r"=(.*)$", profile.read_text(), re.M)
    return found[-1].strip().strip('"').strip("'") if found else ""


def fail(code, message):
    json.dump({"error": message}, sys.stdout)
    sys.stdout.write("\n")
    sys.exit(code)


def configured():
    """Return (config, problem).

    "Not configured" and "turned off" collapse into the same exit code for the
    same reason the CLI collapses them: from here, nothing will be embedded and
    the caller has to fall back. A *partly* configured provider is different — it
    is someone's mistake, and the message names the missing variables (never
    their values) so it can be fixed.
    """
    if from_environment(ENV_SWITCH).strip().lower() == OFF:
        return None, "%s=%s: embeddings are turned off, so a recall can only use the keyword arm" % (
            ENV_SWITCH, OFF)
    values = {
        ENV_BASE_URL: from_environment(ENV_BASE_URL).strip(),
        ENV_MODEL: from_environment(ENV_MODEL).strip(),
        ENV_API_KEY: from_environment(ENV_API_KEY).strip(),
    }
    dimensions = from_environment(ENV_DIMENSIONS).strip()
    values[ENV_DIMENSIONS] = dimensions
    missing = sorted(name for name, value in values.items() if not value)
    if len(missing) == len(values):
        return None, ("no embedding provider is configured, so a recall can only use the keyword arm; "
                      "set %s, %s, %s and %s to add the vector arm" % (
                          ENV_BASE_URL, ENV_MODEL, ENV_DIMENSIONS, ENV_API_KEY))
    if missing:
        return None, ("embedding is partly configured; also set %s, or set %s=%s to turn it off" % (
            ", ".join(missing), ENV_SWITCH, OFF))
    try:
        width = int(dimensions)
    except ValueError:
        return None, "%s must be a positive integer" % ENV_DIMENSIONS
    if width <= 0:
        return None, "%s must be a positive integer" % ENV_DIMENSIONS
    return {
        "base_url": values[ENV_BASE_URL],
        "model": values[ENV_MODEL],
        "api_key": values[ENV_API_KEY],
        "dimensions": width,
    }, ""


def encode_vector(vector):
    """Raw URL-safe base64 of little-endian float32, no padding — the engine's form."""
    for position, value in enumerate(vector):
        if math.isnan(value) or math.isinf(value):
            fail(EXIT_BAD_INPUT, "a vector cannot carry NaN or infinity (position %d)" % position)
    if not vector:
        fail(EXIT_BAD_INPUT, "a vector cannot be empty")
    payload = struct.pack("<%df" % len(vector), *vector)
    return base64.urlsafe_b64encode(payload).decode().rstrip("=")


def read_text():
    try:
        request = json.load(sys.stdin)
    except json.JSONDecodeError as error:
        fail(EXIT_BAD_INPUT, "the request must be JSON on stdin: %s" % error)
    text = request.get("text", "")
    if not isinstance(text, str) or not text.strip():
        fail(EXIT_BAD_INPUT, "the request needs a non-empty text")
    return text


def build_payload(text, config):
    return {"model": config["model"], "input": [text], "dimensions": config["dimensions"]}


def call_provider(config, payload):
    body = json.dumps(payload).encode()
    endpoint = config["base_url"].rstrip("/") + "/embeddings"
    request = urllib.request.Request(
        endpoint, data=body,
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + config["api_key"]})
    try:
        with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT_SECONDS) as response:
            return json.loads(response.read()), None
    except urllib.error.HTTPError as error:
        # The body is read and dropped: a provider that echoes the request on
        # failure would otherwise put the key in a log.
        detail = error.read()[:200]
        return None, "the provider answered %s %s: %s" % (
            error.code, error.reason, detail.decode("utf-8", "replace").strip())
    except urllib.error.URLError as error:
        return None, "could not reach the provider: %s" % error.reason


def main():
    parser = argparse.ArgumentParser(description="Embed a query for RECALL ... NEAREST")
    parser.add_argument("--dry-run", action="store_true",
                        help="print the request that would be sent, send nothing")
    parser.add_argument("--encode", default=None,
                        help="encode a comma separated list of floats instead of calling a provider")
    arguments = parser.parse_args()

    if arguments.encode is not None:
        try:
            vector = [float(part) for part in arguments.encode.split(",")]
        except ValueError:
            fail(EXIT_BAD_INPUT, "--encode takes a comma separated list of numbers")
        json.dump({"vector": encode_vector(vector), "model": "offline", "dimensions": len(vector)}, sys.stdout)
        sys.stdout.write("\n")
        return EXIT_ANSWERED

    config, problem = configured()
    if config is None:
        fail(EXIT_NOT_CONFIGURED, problem)
    text = read_text()
    payload = build_payload(text, config)
    if arguments.dry_run:
        json.dump({"request": payload, "endpoint": "/embeddings", "model": config["model"],
                   "dimensions": config["dimensions"]}, sys.stdout)
        sys.stdout.write("\n")
        return EXIT_ANSWERED

    started = time.monotonic()
    answer, error = call_provider(config, payload)
    elapsed_ms = int((time.monotonic() - started) * 1000)
    if error:
        fail(EXIT_PROVIDER_REFUSED, "%s (after %d ms)" % (error, elapsed_ms))
    entries = answer.get("data")
    if not isinstance(entries, list) or not entries:
        fail(EXIT_PROVIDER_REFUSED, "the provider answered without an embedding")
    ordered = sorted(entries, key=lambda entry: entry.get("index", 0))
    vector = ordered[0].get("embedding")
    if not isinstance(vector, list) or not vector:
        fail(EXIT_PROVIDER_REFUSED, "the provider answered without an embedding")
    values = [float(value) for value in vector]
    if len(values) != config["dimensions"]:
        fail(EXIT_PROVIDER_REFUSED,
             "the provider returned %d dimensions but %s says %d; the Database refuses a vector of "
             "the wrong width, so fix the configuration before using the vector arm" % (
                 len(values), ENV_DIMENSIONS, config["dimensions"]))
    json.dump({"vector": encode_vector(values), "model": config["model"],
               "dimensions": len(values), "elapsed_ms": elapsed_ms}, sys.stdout)
    sys.stdout.write("\n")
    return EXIT_ANSWERED


if __name__ == "__main__":
    sys.exit(main())
