import importlib.util
import base64
import hashlib
import hmac
import json
import datetime as dt
import secrets
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


def _load_plugin():
    plugin_path = Path(__file__).parents[1] / "adapters" / "qwenpaw" / "plugin.py"
    spec = importlib.util.spec_from_file_location(
        "opskeeper_workflow_projection_under_test", plugin_path
    )
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load plugin.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class WorkflowProjectionTest(unittest.TestCase):
    def setUp(self):
        self.state_directory = tempfile.TemporaryDirectory()
        self.state_path = Path(self.state_directory.name) / "workflow-projector.json"
        self.environment = patch.dict(
            "os.environ",
            {"OPSKEEPER_WORKFLOW_PROJECTOR_STATE_FILE": str(self.state_path)},
            clear=False,
        )
        self.environment.start()
        self.module = _load_plugin()
        self.projector = self.module.WorkflowProjector()

    def tearDown(self):
        self.environment.stop()
        self.state_directory.cleanup()

    def test_manager_authority_stages_project_without_matrix_dependency(self):
        origin = "matrix:!room:hs"
        self.projector.record_request(origin, "incident_id=opskeeper-final-demo")

        expected_steps = (
            ("preview_ready", "review", "in_progress"),
            ("awaiting_approval", "approval", "in_progress"),
            ("repair_dispatched", "repair", "in_progress"),
            ("verifying", "verify", "in_progress"),
            ("recovered", "verify", "completed"),
        )
        for stage, step_id, status in expected_steps:
            with patch.object(
                self.module,
                "_send_matrix_workflow",
                side_effect=RuntimeError("matrix unavailable"),
            ):
                workflow = self.projector.record_authority_stage(
                    origin, "opskeeper-final-demo", stage
                )
                event_id = self.module.asyncio.run(
                    self.module._emit_workflow_projection(origin, workflow)
                )
            self.assertEqual(workflow["runId"], "opskeeper-final-demo")
            self.assertEqual(event_id, "")
            projected = next(step for step in workflow["steps"] if step["id"] == step_id)
            self.assertEqual(projected["status"], status)

    def _authority_message(
        self,
        manager="@manager:hs",
        room="!room:hs",
        incident="opskeeper-final-demo",
        stage="awaiting_approval",
        secret="0123456789abcdef",
        expires_offset=30,
        issued_offset=0,
        nonce=None,
        corrupt=False,
    ):
        now = dt.datetime.now(dt.timezone.utc)
        claims = {
            "manager_id": manager,
            "room_id": room,
            "incident_id": incident,
            "stage": stage,
            "nonce": nonce or secrets.token_hex(16),
            "target_fingerprint": "0123456789abcdef",
            "issued_at": (
                now + dt.timedelta(seconds=issued_offset)
            ).isoformat().replace("+00:00", "Z"),
            "expires_at": (
                now + dt.timedelta(seconds=expires_offset)
            ).isoformat().replace("+00:00", "Z"),
        }
        encoded = json.dumps(claims, separators=(",", ":")).encode()
        signature = hmac.new(secret.encode(), encoded, hashlib.sha256).hexdigest()
        if corrupt:
            signature = ("0" if signature[0] != "0" else "1") + signature[1:]
        token = base64.urlsafe_b64encode(encoded).rstrip(b"=").decode() + "." + signature
        return "OPSKEEPER_AUTHORITY_V1 " + token

    def test_authority_token_requires_manager_identity_and_signature(self):
        environment = patch.dict(
            "os.environ",
            {
                "OPSKEEPER_WORKFLOW_AUTHORITY_MANAGER_ID": "@manager:hs",
                "OPSKEEPER_WORKFLOW_AUTHORITY_SECRET": "0123456789abcdef",
            },
            clear=False,
        )
        environment.start()
        try:
            message = self._authority_message()
            self.assertEqual(
                self.module._verify_workflow_authority(
                    "@manager:hs", message, "matrix:!room:hs"
                ),
                ("opskeeper-final-demo", "awaiting_approval"),
            )
            self.assertIsNone(
                self.module._verify_workflow_authority(
                    "@user:hs", message, "matrix:!room:hs"
                )
            )
            self.assertIsNone(
                self.module._verify_workflow_authority(
                    "@manager:hs",
                    self._authority_message(corrupt=True),
                    "matrix:!room:hs",
                )
            )
            self.assertIsNone(
                self.module._verify_workflow_authority(
                    "@manager:hs",
                    self._authority_message(expires_offset=-1),
                    "matrix:!room:hs",
                )
            )
            self.assertIsNone(
                self.module._verify_workflow_authority(
                    "@manager:hs",
                    "workflow_stage=awaiting_approval",
                    "matrix:!room:hs",
                )
            )
        finally:
            environment.stop()

    def test_missing_authority_config_fails_closed(self):
        environment = patch.dict(
            "os.environ",
            {
                "OPSKEEPER_WORKFLOW_AUTHORITY_MANAGER_ID": "",
                "OPSKEEPER_WORKFLOW_AUTHORITY_SECRET": "",
            },
            clear=False,
        )
        environment.start()
        try:
            self.assertIsNone(
                self.module._verify_workflow_authority(
                    "@manager:hs",
                    self._authority_message(),
                    "matrix:!room:hs",
                )
            )
            self.assertIsNone(
                self.module._verify_workflow_authority(
                    "@manager:hs",
                    self._authority_message(secret="short-secret"),
                    "matrix:!room:hs",
                )
            )
            self.assertIsNone(
                self.projector.record_request(
                    "matrix:!room:hs",
                    "incident_id=opskeeper-final-demo workflow_stage=recovered",
                )
                )
        finally:
            environment.stop()

    def test_authority_token_binds_room_and_is_consumed_once(self):
        environment = patch.dict(
            "os.environ",
            {
                "OPSKEEPER_WORKFLOW_AUTHORITY_MANAGER_ID": "@manager:hs",
                "OPSKEEPER_WORKFLOW_AUTHORITY_SECRET": "0123456789abcdef",
            },
            clear=False,
        )
        environment.start()
        self.module._WORKFLOW_AUTHORITY_NONCES.clear()
        try:
            nonce = secrets.token_hex(16)
            message = self._authority_message(nonce=nonce) + "\nworkflow_stage=recovered"
            self.assertIsNone(
                self.module._verify_workflow_authority(
                    "@manager:hs", message, "matrix:!other-room:hs"
                )
            )
            self.assertEqual(
                self.module._verify_workflow_authority(
                    "@manager:hs", message, "matrix:!room:hs"
                ),
                ("opskeeper-final-demo", "awaiting_approval"),
            )
            self.assertIsNone(
                self.module._verify_workflow_authority(
                    "@manager:hs", message, "matrix:!room:hs"
                )
            )
            self.assertIsNone(
                self.module._verify_workflow_authority(
                    "@manager:hs", message, "matrix:!other-room:hs"
                )
            )
        finally:
            self.module._WORKFLOW_AUTHORITY_NONCES.clear()
            environment.stop()

    def test_authority_token_requires_strict_timestamp_window(self):
        environment = patch.dict(
            "os.environ",
            {
                "OPSKEEPER_WORKFLOW_AUTHORITY_MANAGER_ID": "@manager:hs",
                "OPSKEEPER_WORKFLOW_AUTHORITY_SECRET": "0123456789abcdef",
            },
            clear=False,
        )
        environment.start()
        self.module._WORKFLOW_AUTHORITY_NONCES.clear()
        try:
            for issued_offset, expires_offset in ((10, 30), (0, 121)):
                self.assertIsNone(
                    self.module._verify_workflow_authority(
                        "@manager:hs",
                        self._authority_message(
                            issued_offset=issued_offset,
                            expires_offset=expires_offset,
                        ),
                        "matrix:!room:hs",
                    )
                )
        finally:
            self.module._WORKFLOW_AUTHORITY_NONCES.clear()
            environment.stop()


if __name__ == "__main__":
    unittest.main()
