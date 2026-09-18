import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


def _load_plugin():
    path = Path(__file__).with_name("plugin.py")
    spec = importlib.util.spec_from_file_location(
        "opskeeper_workflow_projector_under_test", path
    )
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load plugin.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class WorkflowProjectorTest(unittest.TestCase):
    def setUp(self):
        self.state_directory = tempfile.TemporaryDirectory()
        self.state_patch = patch.dict(
            "os.environ",
            {
                "OPSKEEPER_WORKFLOW_PROJECTOR_STATE_FILE": str(
                    Path(self.state_directory.name) / "workflow-projector.json"
                )
            },
            clear=False,
        )
        self.state_patch.start()
        self.module = _load_plugin()
        self.projector = self.module.WorkflowProjector()

    def tearDown(self):
        self.state_patch.stop()
        self.state_directory.cleanup()

    def test_request_creates_stable_dashboard_payload(self):
        payload = self.projector.record_request(
            "matrix:!room:hs",
            "@manager 请处理 incident_id=opskeeper-final-demo",
        )
        self.assertEqual(payload["runId"], "opskeeper-final-demo")
        self.assertEqual(payload["status"], "assigned")
        self.assertEqual(payload["steps"][0]["id"], "alert")
        self.assertEqual(payload["steps"][0]["status"], "pending")
        self.assertTrue(payload["subagents"])
        self.assertIsNone(
            self.projector.record_request(
                "matrix:!room:hs",
                "@manager 请处理 incident_id=opskeeper-final-demo",
            )
        )

    def test_dispatch_result_and_approval_advance_stages(self):
        origin = "matrix:!room:hs"
        self.projector.record_request(origin, "incident_id=opskeeper-stage-demo")
        dispatch = self.projector.record_dispatch(
            origin,
            "@opskeeper-alerter:hs OPSKEEPER TASK OPSKEEPER-STAGE-ALERT",
        )
        self.assertEqual(dispatch["steps"][0]["status"], "in_progress")

        result = self.projector.record_result(
            "OPSKEEPER-STAGE-ALERT",
            "@manager:hs OPSKEEPER_RESULT OPSKEEPER-STAGE-ALERT {\"status\":\"completed\"}",
        )
        self.assertEqual(result["steps"][0]["status"], "completed")

        for role, marker, step_index in (
            ("investigator", "OPSKEEPER-STAGE-INVESTIGATE", 1),
            ("reviewer", "OPSKEEPER-STAGE-REVIEW", 2),
        ):
            self.projector.record_dispatch(
                origin,
                f"@opskeeper-{role}:hs OPSKEEPER TASK {marker}",
            )
            result = self.projector.record_result(
                marker,
                f"@manager:hs OPSKEEPER_RESULT {marker} {{\"status\":\"completed\"}}",
            )
            self.assertEqual(result["steps"][step_index]["status"], "completed")

        self.assertEqual(result["steps"][3]["status"], "pending")
        self.assertIn(
            "Repair preview evidence required",
            result["summary"],
        )
        self.projector.record_authority_stage(
            origin,
            "opskeeper-stage-demo",
            "awaiting_approval",
        )
        self.assertEqual(
            self.projector.payload("opskeeper-stage-demo")["steps"][3]["status"],
            "in_progress",
        )
        approved = self.projector.record_admin_decision(
            origin,
            "@manager:hs incident_id=opskeeper-stage-demo 批准",
            True,
        )
        self.assertEqual(approved["steps"][3]["status"], "completed")
        self.assertEqual(approved["status"], "in_progress")

    def test_authority_stages_project_final_demo_progression(self):
        origin = "matrix:!room:hs"
        self.projector.record_request(origin, "incident_id=opskeeper-authority-demo")

        expectations = (
            ("preview_ready", 2, "in_progress"),
            ("awaiting_approval", 3, "in_progress"),
            ("repair_dispatched", 4, "in_progress"),
            ("verifying", 5, "in_progress"),
            ("recovered", 5, "completed"),
        )
        for stage, step_index, step_status in expectations:
            payload = self.projector.record_authority_stage(
                origin,
                "opskeeper-authority-demo",
                stage,
            )
            self.assertEqual(payload["steps"][step_index]["status"], step_status)
            self.assertEqual(payload["status"], "in_progress")

    def test_matrix_content_contains_element_body_and_workflow_object(self):
        self.projector.record_request(
            "matrix:!room:hs",
            "incident_id=opskeeper-matrix-demo",
        )
        workflow = self.projector.payload("opskeeper-matrix-demo")
        captured = {}

        class Response:
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read(self):
                return json.dumps({"event_id": "$workflow-event"}).encode()

        def urlopen(request, timeout):
            captured["path"] = request.full_url
            captured["body"] = json.loads(request.data.decode("utf-8"))
            captured["timeout"] = timeout
            return Response()

        with patch.object(self.module.urllib.request, "urlopen", side_effect=urlopen):
            matrix_patch = patch.dict(
                "os.environ",
                {
                    "AGENTTEAMS_MATRIX_URL": "http://matrix.test",
                    "AGENTTEAMS_MANAGER_MATRIX_TOKEN": "test-token",
                },
                clear=False,
            )
            matrix_patch.start()
            try:
                event_id = self.module._send_matrix_workflow(
                    "matrix:!room:hs",
                    workflow,
                )
            finally:
                matrix_patch.stop()
        self.assertEqual(event_id, "$workflow-event")
        self.assertIn("/_matrix/client/v3/rooms/", captured["path"])
        self.assertIn("[OpsKeeper Workflow]", captured["body"]["body"])
        self.assertEqual(
            captured["body"]["agentteams.workflow"]["runId"],
            "opskeeper-matrix-demo",
        )

    def test_matrix_failure_does_not_raise(self):
        with patch.object(
            self.module,
            "_send_matrix_workflow",
            side_effect=RuntimeError("matrix unavailable"),
        ):
            event_id = self.module.asyncio.run(
                self.module._emit_workflow_projection(
                    "matrix:!room:hs",
                    {"runId": "demo"},
                )
            )


if __name__ == "__main__":
    unittest.main(verbosity=2)
