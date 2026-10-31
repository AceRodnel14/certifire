#!/usr/bin/env python3
"""Local webhook stub. TLS on :443 (Discord/Teams hosts), control API on :9000 (plain).
Control: POST /__mode {"status":204,"retryAfter":7,"redirect":false,"bigBody":false}
         GET /__requests -> recorded webhook requests; POST /__reset clears.
Fake only: never forwards anything anywhere."""

import json
import ssl
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATE = {"mode": {"status": 204}, "reqs": []}


class Hook(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n).decode("utf-8", "replace")
        STATE["reqs"].append(
            {"host": self.headers.get("Host"), "path": self.path, "body": body}
        )
        m = STATE["mode"]
        if m.get("redirect"):
            self.send_response(302)
            self.send_header("Location", "https://example.invalid/x")
            self.end_headers()
            return
        self.send_response(m.get("status", 204))
        if m.get("retryAfter"):
            self.send_header("Retry-After", str(m["retryAfter"]))
        payload = b"x" * 1048576 if m.get("bigBody") else b""
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        try:
            self.wfile.write(payload)
        except OSError:
            self.log_error("client went away")


class Ctl(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _j(self, o):
        b = json.dumps(o).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        self._j(STATE["reqs"])

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) or b"{}"
        if self.path == "/__mode":
            STATE["mode"] = json.loads(raw)
        if self.path == "/__reset":
            STATE["reqs"].clear()
            STATE["mode"] = {"status": 204}
        self._j({"ok": True})


ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain("/stub/stub.crt", "/stub/stub.key")
https = ThreadingHTTPServer(("0.0.0.0", 443), Hook)
https.socket = ctx.wrap_socket(https.socket, server_side=True)
threading.Thread(target=https.serve_forever, daemon=True).start()
ThreadingHTTPServer(("0.0.0.0", 9000), Ctl).serve_forever()
