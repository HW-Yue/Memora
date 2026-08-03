import asyncio
import hashlib
import importlib.util
import json
import os
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("evaluate.py")


def load_adapter():
    spec = importlib.util.spec_from_file_location("memora_ragas_evaluate", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def digest(value):
    copied = dict(value)
    copied["hash"] = ""
    encoded = json.dumps(copied, ensure_ascii=False, separators=(",", ":"))
    return "sha256:" + hashlib.sha256(encoded.encode("utf-8")).hexdigest()


def evaluation_input():
    sha = "sha256:" + "a" * 64
    value = {
        "version": "memora.external-evaluation-input/v1",
        "run_id": "run-test",
        "corpus_id": "corpus-test",
        "corpus_revision": 1,
        "snapshot_sha256": sha,
        "public_scorecard_sha256": sha,
        "private_diagnostics_sha256": sha,
        "ground_truth_sha256": sha,
        "cases": [
            {
                "case_id": "case-001",
                "runner_status": "succeeded",
                "user_input": "Question one?",
                "response": "Answer one.",
                "reference": "Reference one.",
                "retrieved_contexts": ['{"fact":"one"}'],
            },
            {
                "case_id": "case-002",
                "runner_status": "succeeded",
                "user_input": "Question two?",
                "response": "Answer two.",
                "reference": "Reference two.",
                "retrieved_contexts": ['{"fact":"two"}'],
            },
            {
                "case_id": "case-003",
                "runner_status": "failed",
                "user_input": "Question three?",
                "reference": "Reference three.",
                "retrieved_contexts": [],
            },
        ],
        "hash": "",
    }
    value["hash"] = digest(value)
    return value


class FakeMetric:
    def __init__(self, name, value, calls, fail_question=None):
        self.name = name
        self.value = value
        self.calls = calls
        self.fail_question = fail_question

    async def ascore(self, **kwargs):
        self.calls.append((self.name, kwargs))
        if self.fail_question is not None and self.fail_question in {
            kwargs.get("user_input"), kwargs.get("response")
        }:
            raise RuntimeError("synthetic judge failure")
        return SimpleNamespace(value=self.value)


class EvaluateTest(unittest.TestCase):
    def test_scores_success_and_isolates_runner_and_metric_failures(self):
        adapter = load_adapter()
        calls = []

        def metrics_factory(_configuration):
            return {
                "factual_correctness": FakeMetric(
                    "factual_correctness", 0.9, calls, "Answer two."
                ),
                "faithfulness": FakeMetric("faithfulness", 0.8, calls),
                "context_precision": FakeMetric("context_precision", 0.7, calls),
                "context_recall": FakeMetric("context_recall", 0.6, calls),
            }

        environment = {
            "MEMORA_EVAL_API_BASE_URL": "https://judge.invalid/v1",
            "MEMORA_EVAL_MODEL": "judge-test",
            "MEMORA_EVAL_SECRET_ENV": "MEMORA_TEST_KEY",
            "MEMORA_TEST_KEY": "private-test-secret",
        }
        with mock.patch.dict(os.environ, environment, clear=True):
            output = asyncio.run(
                adapter.evaluate_document(evaluation_input(), metrics_factory)
            )

        self.assertEqual(output["version"], "memora.external-evaluator-output/v1")
        self.assertEqual(output["adapter_id"], "ragas-collections")
        self.assertEqual(output["adapter_version"], "0.4.3")
        self.assertEqual(output["judge_model"], "judge-test")
        self.assertEqual(output["hash"], "")
        self.assertEqual(output["cases"][0]["status"], "scored")
        self.assertEqual(
            output["cases"][0]["scores"],
            {
                "factual_correctness": 0.9,
                "faithfulness": 0.8,
                "context_precision": 0.7,
                "context_recall": 0.6,
            },
        )
        self.assertEqual(output["cases"][1]["status"], "evaluator_failed")
        self.assertEqual(output["cases"][1]["error_code"], "metric_failed")
        self.assertEqual(output["cases"][1]["scores"], {})
        self.assertEqual(output["cases"][2], {
            "case_id": "case-003", "status": "runner_failed", "scores": {}
        })
        self.assertEqual(len(calls), 5)
        self.assertEqual(calls[0][1], {
            "response": "Answer one.", "reference": "Reference one."
        })
        self.assertEqual(calls[1][1], {
            "user_input": "Question one?", "response": "Answer one.",
            "retrieved_contexts": ['{"fact":"one"}'],
        })

    def test_rejects_unknown_or_tampered_input(self):
        adapter = load_adapter()
        value = evaluation_input()
        value["unknown"] = True
        with self.assertRaises(adapter.ProtocolError):
            adapter.validate_input(value)
        value = evaluation_input()
        value["cases"][0]["reference"] = "tampered"
        with self.assertRaises(adapter.ProtocolError):
            adapter.validate_input(value)


if __name__ == "__main__":
    unittest.main()
