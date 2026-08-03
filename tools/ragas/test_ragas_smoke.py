import asyncio
import importlib.util
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
import threading
import unittest
from unittest import mock

from tools.ragas import evaluate


RAGAS_AVAILABLE = importlib.util.find_spec("ragas") is not None


def judge_payload(request):
    messages = json.dumps(request.get("messages", []), ensure_ascii=False)
    if "ContextRecallOutput" in messages:
        return {
            "classifications": [{
                "statement": "The fact is stored.",
                "reason": "supported",
                "attributed": 1,
            }]
        }
    if "ContextPrecisionOutput" in messages:
        return {"reason": "supported", "verdict": 1}
    if "NLIStatementOutput" in messages:
        return {
            "statements": [{
                "statement": "The fact is stored.",
                "reason": "supported",
                "verdict": 1,
            }]
        }
    if "StatementGeneratorOutput" in messages:
        return {"statements": ["The fact is stored."]}
    if "ClaimDecompositionOutput" in messages:
        return {"claims": ["The fact is stored."]}
    return {}


@unittest.skipUnless(RAGAS_AVAILABLE, "Ragas v0.4.3 is not installed")
class RagasCollectionsSmokeTest(unittest.TestCase):
    def test_real_collections_api_against_fake_openai_judge(self):
        requests = []
        authorizations = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                authorizations.append(self.headers.get("Authorization"))
                requests.append(body)
                response = {
                    "id": "fake",
                    "object": "chat.completion",
                    "created": 1,
                    "model": body.get("model", "judge-test"),
                    "choices": [{
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": json.dumps(judge_payload(body)),
                        },
                        "finish_reason": "stop",
                    }],
                    "usage": {
                        "prompt_tokens": 10,
                        "completion_tokens": 2,
                        "total_tokens": 12,
                    },
                }
                encoded = json.dumps(response).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

            def log_message(self, *_arguments):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        sha = "sha256:" + "a" * 64
        document = {
            "version": evaluate.INPUT_VERSION,
            "run_id": "run-smoke",
            "corpus_id": "corpus-smoke",
            "corpus_revision": 1,
            "snapshot_sha256": sha,
            "public_scorecard_sha256": sha,
            "private_diagnostics_sha256": sha,
            "ground_truth_sha256": sha,
            "cases": [{
                "case_id": "case-001",
                "runner_status": "succeeded",
                "user_input": "What is stored?",
                "response": "The fact is stored.",
                "reference": "The fact is stored.",
                "retrieved_contexts": ['{"fact":"The fact is stored."}'],
            }],
            "hash": "",
        }
        document["hash"] = evaluate.document_digest(document)
        environment = {
            "MEMORA_EVAL_API_BASE_URL": f"http://127.0.0.1:{server.server_port}/v1",
            "MEMORA_EVAL_MODEL": "judge-test",
            "MEMORA_EVAL_SECRET_ENV": "MEMORA_SMOKE_KEY",
            "MEMORA_SMOKE_KEY": "local-secret",
        }
        try:
            with mock.patch.dict(os.environ, environment, clear=False):
                output = asyncio.run(evaluate.evaluate_document(document))
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

        self.assertEqual(output["cases"][0]["status"], "scored")
        self.assertEqual(output["cases"][0]["scores"], {
            "factual_correctness": 1.0,
            "faithfulness": 1.0,
            "context_precision": 0.9999999999,
            "context_recall": 1.0,
        })
        self.assertEqual(len(requests), 8)
        self.assertTrue(all(request["model"] == "judge-test" for request in requests))
        self.assertTrue(all(value == "Bearer local-secret" for value in authorizations))


if __name__ == "__main__":
    unittest.main()
