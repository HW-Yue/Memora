#!/usr/bin/env python3
"""Prove that a walk's provider connection is reused, not rebuilt per decision.

The saving is the point of the change (696-804 ms per call on a fresh TLS
connection against 244-523 ms on a reused one), and it is the kind of behaviour
that quietly stops happening — a `close()` in the wrong place, or a request built
without a length — while every functional test still passes. So this starts a
local HTTP/1.1 server that behaves like a keep-alive provider, asks it twice on
one `Provider`, and counts the connections the server accepted.

    python3 keepalive_check.py <path-to-jev_select.py>

Exit 0 with `connections: 1` when the second request travelled on the first
connection; non-zero, with what it saw, when it did not.
"""

import importlib.util
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# Loading the script must not leave a `__pycache__` inside the shipped Skill tree,
# which is compared byte for byte by another gate.
sys.dont_write_bytecode = True

if len(sys.argv) != 2:
    sys.stderr.write("usage: keepalive_check.py <path-to-jev_select.py>\n")
    sys.exit(2)

spec = importlib.util.spec_from_file_location("jev_select", sys.argv[1])
jev_select = importlib.util.module_from_spec(spec)
spec.loader.exec_module(jev_select)

connections = 0
lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    # HTTP/1.1 is what makes the connection reusable at all; an HTTP/1.0 server
    # closes after every response and would hide the behaviour under test.
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        self.rfile.read(length)
        body = json.dumps({"model": "fake-1.0", "answers": {
            "holds_0": {"type": "noul", "noul": 0.92},
            "holds_1": {"type": "noul", "noul": 0.08},
            "nothing_here": {"type": "noul", "noul": 0.04},
        }}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *arguments):
        pass


class Server(ThreadingHTTPServer):
    def process_request(self, request, client_address):
        global connections
        with lock:
            connections += 1
        super().process_request(request, client_address)


server = Server(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
port = server.server_address[1]

provider = jev_select.Provider(base_url="http://127.0.0.1:%d" % port, api_key="test", model="fake")
payload = jev_select.build_set_payload("does the intent belong here", {"work": "work facts", "notes": "notes"}, "fake")
first, error = provider.send(payload)
if error:
    sys.stderr.write("first request failed: %s\n" % error)
    sys.exit(1)
second, error = provider.send(payload)
if error:
    sys.stderr.write("second request failed: %s\n" % error)
    sys.exit(1)
provider.close()
server.shutdown()

if first["answers"]["holds_0"]["noul"] != 0.92 or second["answers"]["holds_0"]["noul"] != 0.92:
    sys.stderr.write("the answers did not come back intact: %r %r\n" % (first, second))
    sys.exit(1)

print("connections: %d" % connections)
if connections != 1:
    sys.stderr.write("two requests opened %d connections: the provider is not being reused\n" % connections)
    sys.exit(1)
