import importlib.util
import io
import json
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import patch


def _load_plugin():
    path = Path(__file__).with_name("plugin.py")
    spec = importlib.util.spec_from_file_location("opskeeper_final_demo_approval_under_test", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load plugin.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FinalDemoApprovalTest(unittest.TestCase):
    def setUp(self):
        self.state_directory = tempfile.TemporaryDirectory()
        environment = {
            "OPSKEEPER_DEMO_MATRIX_ROOM": "!room:hs",
            "OPSKEEPER_MANAGER_URL": "http://opskeeper:8080",
            "OPSKEEPER_DEMO_API_TOKEN": "demo-token",
            "OPSKEEPER_WORKFLOW_PROJECTOR_STATE_FILE": str(
                Path(self.state_directory.name) / "workflow-projector.json"
            ),
        }
        self.environment_patch = patch.dict("os.environ", environment, clear=False)
        self.environment_patch.start()
        self.module = _load_plugin()

    def tearDown(self):
        self.environment_patch.stop()
        self.state_directory.cleanup()

    def test_dispatches_admin_approval_to_manager(self):
        requests = []

        class Response:
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read(self):
                return b"{}"

        def urlopen(request, timeout):
            requests.append((request, timeout))
            return Response()

        with patch("urllib.request.urlopen", urlopen):
            accepted = self.module._dispatch_final_demo_approval(
                "matrix:!room:hs",
                "@admin:hs",
                "@manager:hs 已批准\nincident_id=82",
            )

        self.assertTrue(accepted)
        self.assertEqual(len(requests), 1)
        request, timeout = requests[0]
        self.assertEqual(request.full_url, "http://opskeeper:8080/api/v1/demo/incidents/82/approve")
        self.assertEqual(request.get_header("Authorization"), "Bearer demo-token")
        self.assertEqual(request.get_header("X-opskeeper-version"), "v1")
        self.assertEqual(
            json.loads(request.data.decode()),
            {"approver_id": "@admin:hs"},
        )
        self.assertGreaterEqual(timeout, 5)

    def test_dispatch_retries_transient_server_errors(self):
        requests = []

        class Response:
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read(self):
                return b"{}"

        class ServiceUnavailable(urllib.error.HTTPError):
            def __init__(self, url):
                super().__init__(url, 503, "service unavailable", None, io.BytesIO(b"{}"))

        def urlopen(request, timeout):
            requests.append((request, timeout))
            if len(requests) == 1:
                raise ServiceUnavailable(request.full_url)
            return Response()

        with patch("urllib.request.urlopen", urlopen), patch("time.sleep") as sleep:
            accepted = self.module._dispatch_final_demo_approval(
                "matrix:!room:hs",
                "@admin:hs",
                "@manager:hs 已批准\nincident_id=82",
            )

        self.assertTrue(accepted)
        self.assertEqual(len(requests), 2)
        self.assertEqual(sleep.call_count, 1)

    def test_dispatch_uses_current_message_when_history_contains_old_incident(self):
        requests = []

        class Response:
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read(self):
                return b"{}"

        def urlopen(request, timeout):
            requests.append((request, timeout))
            return Response()

        message = (
            "[Chat messages since your last reply - for context]\n"
            "admin: @manager:hs 已拒绝\nincident_id=82\n\n"
            "[Current message - respond to this]\n"
            "admin: @manager:hs 已批准\nincident_id=89"
        )
        current_message = self.module._current_matrix_message(message)
        self.assertTrue(self.module._WORKFLOW_ADMIN_APPROVAL_PATTERN.search(current_message))
        self.assertIsNone(self.module._WORKFLOW_ADMIN_REJECTION_PATTERN.search(current_message))

        with patch("urllib.request.urlopen", urlopen):
            accepted = self.module._dispatch_final_demo_approval(
                "matrix:!room:hs",
                "@admin:hs",
                message,
            )

        self.assertTrue(accepted)
        self.assertEqual(len(requests), 1)
        request, _timeout = requests[0]
        self.assertTrue(request.full_url.endswith("/api/v1/demo/incidents/89/approve"))
        self.assertEqual(self.module._final_demo_incident_id(message), "89")

    def test_rejects_other_rooms_without_http_call(self):
        with patch("urllib.request.urlopen", side_effect=AssertionError("must not call")):
            accepted = self.module._dispatch_final_demo_approval(
                "matrix:!other:hs",
                "@admin:hs",
                "@manager:hs 已批准\nincident_id=82",
            )
        self.assertFalse(accepted)

    def test_approval_pattern_accepts_matrix_display_prefix(self):
        message = "Mmanager: @manager:matrix-local.agentteams.io:18080 已批准\nincident_id=82"

        self.assertIsNotNone(self.module._WORKFLOW_ADMIN_APPROVAL_PATTERN.search(message))
        self.assertIsNone(self.module._WORKFLOW_ADMIN_REJECTION_PATTERN.search(message))


if __name__ == "__main__":
    unittest.main()
