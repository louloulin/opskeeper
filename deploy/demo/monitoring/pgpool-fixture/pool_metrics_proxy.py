#!/usr/bin/env python3
"""Authenticated Prometheus proxy for all authoritative pool fixture metrics."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import urllib.error
import urllib.request


class PoolMetricsProxy(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/healthz":
            self._write(200, b"ok\n", "text/plain; charset=utf-8")
            return
        if self.path != "/metrics":
            self._write(404, b"not found\n", "text/plain; charset=utf-8")
            return

        try:
            token = Path(os.environ.get("POOL_FIXTURE_TOKEN_FILE", "/var/run/secrets/pool-token")).read_text(encoding="utf-8").strip()
            if not token:
                raise RuntimeError("empty pool fixture token")
            upstream_url = os.environ.get("POOL_FIXTURE_URL", "http://pool-fixture:8092").rstrip("/")
            request = urllib.request.Request(
                upstream_url + "/metrics",
                headers={"Authorization": f"Bearer {token}"},
            )
            with urllib.request.urlopen(request, timeout=3) as response:
                body = response.read()
                content_type = response.headers.get("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
                self._write(response.status, body, content_type)
        except (OSError, UnicodeError, urllib.error.URLError, RuntimeError):
            self._write(503, b"pool fixture unavailable\n", "text/plain; charset=utf-8")

    def _write(self, status, body, content_type):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        return


if __name__ == "__main__":
    port = int(os.environ.get("POOL_METRICS_PORT", "8094"))
    ThreadingHTTPServer(("0.0.0.0", port), PoolMetricsProxy).serve_forever()
