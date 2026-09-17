#!/usr/bin/env python3

import os
import tempfile
import threading
import unittest
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import pool_metrics_proxy


class StubUpstream:
    def __init__(self, body=b"", status=200):
        self.body = body
        self.status = status
        self.last_authorization = ""
        self.server = None

    def __enter__(self):
        upstream = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                upstream.last_authorization = self.headers.get("Authorization", "")
                self.send_response(upstream.status)
                self.send_header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
                self.send_header("Content-Length", str(len(upstream.body)))
                self.end_headers()
                self.wfile.write(upstream.body)

            def log_message(self, _format, *_args):
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        self._thread = thread
        return f"http://127.0.0.1:{self.server.server_port}"

    def __exit__(self, *_args):
        self.server.shutdown()
        self.server.server_close()
        self._thread.join(timeout=2)


class PoolMetricsProxyTest(unittest.TestCase):
    def test_metrics_is_authenticated_pass_through(self):
        body = b"opskeeper_pool_fixture_active_connections{} 2\n"
        upstream = StubUpstream(body)
        with upstream as base_url:
            with self._proxy(base_url) as proxy_url:
                status, content_type, response_body = self._get(proxy_url + "/metrics")
        self.assertEqual(status, 200)
        self.assertEqual(content_type, "text/plain")
        self.assertEqual(response_body, body)
        self.assertEqual(upstream.last_authorization, "Bearer test-token-1234567890")

    def test_upstream_failure_returns_503_without_pool_data(self):
        with StubUpstream(status=500) as base_url:
            with self._proxy(base_url) as proxy_url:
                status, _, response_body = self._get(proxy_url + "/metrics")
        self.assertEqual(status, 503)
        self.assertNotIn(b"opskeeper_pool_fixture", response_body)

    def test_health_is_local_and_requires_no_fixture(self):
        with StubUpstream() as base_url:
            with self._proxy(base_url) as proxy_url:
                status, _, response_body = self._get(proxy_url + "/healthz")
        self.assertEqual(status, 200)
        self.assertEqual(response_body, b"ok\n")

    def test_proxy_source_does_not_accept_manifest_configuration(self):
        source = Path(pool_metrics_proxy.__file__).read_text(encoding="utf-8")
        self.assertNotIn("POOL_MANIFEST_ID", source)

    @staticmethod
    def _get(url):
        try:
            response = urllib.request.urlopen(url, timeout=3)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, response.headers.get_content_type(), response.read()

    def _proxy(self, base_url):
        proxy = pool_metrics_proxy.PoolMetricsProxy

        class ServerContext:
            def __init__(self):
                self.server = None

            def __enter__(self):
                self.server = ThreadingHTTPServer(("127.0.0.1", 0), proxy)
                thread = threading.Thread(target=self.server.serve_forever, daemon=True)
                thread.start()
                self._thread = thread
                return f"http://127.0.0.1:{self.server.server_port}"

            def __exit__(self, *_args):
                self.server.shutdown()
                self.server.server_close()
                self._thread.join(timeout=2)

        token_file_context = tempfile.NamedTemporaryFile(
            "w", encoding="utf-8", delete=False
        )
        token_file_context.write("test-token-1234567890")
        token_file_context.close()
        environment = {
            "POOL_FIXTURE_URL": base_url,
            "POOL_FIXTURE_TOKEN_FILE": token_file_context.name,
        }
        return _ProxyContext(ServerContext(), environment, token_file_context.name)


class _ProxyContext:
    def __init__(self, server_context, environment, token_path):
        self.server_context = server_context
        self.environment = environment
        self.token_path = token_path
        self.patcher = patch.dict(os.environ, environment, clear=False)

    def __enter__(self):
        self.patcher.start()
        return self.server_context.__enter__()

    def __exit__(self, *_args):
        self.server_context.__exit__(*(_args,))
        self.patcher.stop()
        os.unlink(self.token_path)


if __name__ == "__main__":
    unittest.main()
