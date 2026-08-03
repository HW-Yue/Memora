#!/usr/bin/env python3
"""Ragas v0.4.3 adapter for Memora's external answer-evaluation protocol."""

import argparse
import asyncio
import hashlib
import json
import math
import os
from pathlib import Path
import re
import stat
import sys
from urllib.parse import urlparse


# Evaluation inputs are private. Never allow Ragas usage analytics from this adapter.
os.environ["RAGAS_DO_NOT_TRACK"] = "true"


INPUT_VERSION = "memora.external-evaluation-input/v1"
OUTPUT_VERSION = "memora.external-evaluator-output/v1"
ADAPTER_ID = "ragas-collections"
ADAPTER_VERSION = "0.4.3"
MAXIMUM_DOCUMENT_BYTES = 64 << 20
DIGEST_PATTERN = re.compile(r"^sha256:[0-9a-f]{64}$")
ENVIRONMENT_PATTERN = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")


class ProtocolError(Exception):
    """Raised for an invalid evaluator document or runtime configuration."""


def canonical_json(value):
    encoded = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    return (
        encoded.replace("&", "\\u0026")
        .replace("<", "\\u003c")
        .replace(">", "\\u003e")
        .replace("\u2028", "\\u2028")
        .replace("\u2029", "\\u2029")
    )


def document_digest(document):
    unsealed = dict(document)
    unsealed["hash"] = ""
    digest = hashlib.sha256(canonical_json(unsealed).encode("utf-8")).hexdigest()
    return "sha256:" + digest


def validate_input(document):
    expected = {
        "version",
        "run_id",
        "corpus_id",
        "corpus_revision",
        "snapshot_sha256",
        "public_scorecard_sha256",
        "private_diagnostics_sha256",
        "ground_truth_sha256",
        "cases",
        "hash",
    }
    if not isinstance(document, dict) or set(document) != expected:
        raise ProtocolError("invalid input fields")
    if document["version"] != INPUT_VERSION:
        raise ProtocolError("invalid input version")
    for name in ("run_id", "corpus_id"):
        require_text(document[name], 200)
    if not isinstance(document["corpus_revision"], int) or isinstance(
        document["corpus_revision"], bool
    ) or document["corpus_revision"] <= 0:
        raise ProtocolError("invalid corpus revision")
    for name in (
        "snapshot_sha256",
        "public_scorecard_sha256",
        "private_diagnostics_sha256",
        "ground_truth_sha256",
        "hash",
    ):
        if not isinstance(document[name], str) or not DIGEST_PATTERN.fullmatch(
            document[name]
        ):
            raise ProtocolError("invalid digest")
    cases = document["cases"]
    if not isinstance(cases, list) or not cases:
        raise ProtocolError("invalid cases")
    previous = ""
    for case in cases:
        validate_case(case)
        if case["case_id"] <= previous:
            raise ProtocolError("cases are not strictly ordered")
        previous = case["case_id"]
    if document_digest(document) != document["hash"]:
        raise ProtocolError("input digest mismatch")
    return document


def validate_case(case):
    common = {
        "case_id",
        "runner_status",
        "user_input",
        "reference",
        "retrieved_contexts",
    }
    if not isinstance(case, dict) or case.get("runner_status") not in {
        "succeeded",
        "failed",
    }:
        raise ProtocolError("invalid case")
    expected = common | ({"response"} if case["runner_status"] == "succeeded" else set())
    if set(case) != expected:
        raise ProtocolError("invalid case fields")
    require_text(case["case_id"], 200)
    require_text(case["user_input"], 2048)
    require_text(case["reference"], 1 << 20)
    if case["runner_status"] == "succeeded":
        require_text(case["response"], 1 << 20)
    contexts = case["retrieved_contexts"]
    if not isinstance(contexts, list) or (
        case["runner_status"] == "failed" and contexts
    ):
        raise ProtocolError("invalid contexts")
    for context in contexts:
        require_text(context, 1 << 20)
        try:
            parsed = json.loads(context)
        except (TypeError, ValueError) as error:
            raise ProtocolError("invalid context JSON") from error
        if not isinstance(parsed, dict):
            raise ProtocolError("context must be a JSON object")


def require_text(value, maximum):
    if (
        not isinstance(value, str)
        or not value.strip()
        or len(value.encode("utf-8")) > maximum
        or "\x00" in value
    ):
        raise ProtocolError("invalid text")


def runtime_configuration():
    api_base_url = os.environ.get("MEMORA_EVAL_API_BASE_URL", "")
    model = os.environ.get("MEMORA_EVAL_MODEL", "")
    secret_name = os.environ.get("MEMORA_EVAL_SECRET_ENV", "")
    parsed = urlparse(api_base_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.username:
        raise ProtocolError("invalid API base URL")
    require_text(model, 200)
    if not ENVIRONMENT_PATTERN.fullmatch(secret_name):
        raise ProtocolError("invalid secret environment name")
    api_key = os.environ.get(secret_name, "")
    if not api_key or "\x00" in api_key:
        raise ProtocolError("evaluator secret is unavailable")
    return {
        "api_base_url": api_base_url,
        "model": model,
        "api_key": api_key,
    }


def build_metrics(configuration):
    from openai import AsyncOpenAI
    from ragas.llms import llm_factory
    from ragas.metrics.collections import (
        ContextPrecision,
        ContextRecall,
        FactualCorrectness,
        Faithfulness,
    )

    client = AsyncOpenAI(
        api_key=configuration["api_key"], base_url=configuration["api_base_url"]
    )
    llm = llm_factory(configuration["model"], client=client)
    return {
        "factual_correctness": FactualCorrectness(llm=llm),
        "faithfulness": Faithfulness(llm=llm),
        "context_precision": ContextPrecision(llm=llm),
        "context_recall": ContextRecall(llm=llm),
    }


async def evaluate_document(document, metrics_factory=build_metrics):
    validate_input(document)
    configuration = runtime_configuration()
    successful = any(case["runner_status"] == "succeeded" for case in document["cases"])
    metrics = metrics_factory(configuration) if successful else None
    output = {
        "version": OUTPUT_VERSION,
        "input_sha256": document["hash"],
        "adapter_id": ADAPTER_ID,
        "adapter_version": ADAPTER_VERSION,
        "judge_model": configuration["model"],
        "cases": [],
        "hash": "",
    }
    for case in document["cases"]:
        if case["runner_status"] == "failed":
            output["cases"].append(
                {"case_id": case["case_id"], "status": "runner_failed", "scores": {}}
            )
            continue
        try:
            scores = await score_case(case, metrics)
            output["cases"].append(
                {"case_id": case["case_id"], "status": "scored", "scores": scores}
            )
        except Exception:
            output["cases"].append(
                {
                    "case_id": case["case_id"],
                    "status": "evaluator_failed",
                    "error_code": "metric_failed",
                    "scores": {},
                }
            )
    return output


async def score_case(case, metrics):
    values = {}
    values["factual_correctness"] = metric_value(
        await metrics["factual_correctness"].ascore(
            response=case["response"], reference=case["reference"]
        )
    )
    values["faithfulness"] = metric_value(
        await metrics["faithfulness"].ascore(
            user_input=case["user_input"],
            response=case["response"],
            retrieved_contexts=case["retrieved_contexts"],
        )
    )
    values["context_precision"] = metric_value(
        await metrics["context_precision"].ascore(
            user_input=case["user_input"],
            reference=case["reference"],
            retrieved_contexts=case["retrieved_contexts"],
        )
    )
    values["context_recall"] = metric_value(
        await metrics["context_recall"].ascore(
            user_input=case["user_input"],
            reference=case["reference"],
            retrieved_contexts=case["retrieved_contexts"],
        )
    )
    return values


def metric_value(result):
    try:
        value = float(result.value)
    except (AttributeError, TypeError, ValueError, OverflowError) as error:
        raise ProtocolError("metric returned no finite numeric value") from error
    if not math.isfinite(value) or value < 0 or value > 1:
        raise ProtocolError("metric returned an out-of-range value")
    return value


def reject_duplicate_keys(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ProtocolError("duplicate JSON field")
        value[key] = item
    return value


def read_document(path):
    info = path.lstat()
    if (
        not stat.S_ISREG(info.st_mode)
        or stat.S_IMODE(info.st_mode) & 0o077
        or info.st_size <= 0
        or info.st_size > MAXIMUM_DOCUMENT_BYTES
    ):
        raise ProtocolError("input file is not private regular JSON")
    with path.open("r", encoding="utf-8") as source:
        return json.load(source, object_pairs_hook=reject_duplicate_keys)


def write_document(path, document):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) & 0o077:
        raise ProtocolError("output file is not private regular JSON")
    encoded = canonical_json(document).encode("utf-8")
    if len(encoded) > MAXIMUM_DOCUMENT_BYTES:
        raise ProtocolError("output exceeds size limit")
    with path.open("r+b") as destination:
        destination.seek(0)
        destination.write(encoded)
        destination.truncate()
        destination.flush()
        os.fsync(destination.fileno())


def parse_arguments(arguments):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    return parser.parse_args(arguments)


async def run(arguments):
    parsed = parse_arguments(arguments)
    document = read_document(parsed.input)
    output = await evaluate_document(document)
    write_document(parsed.output, output)


def main():
    try:
        asyncio.run(run(sys.argv[1:]))
    except Exception:
        print("memora Ragas evaluator failed", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
