import importlib.util
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


if __name__ == "__main__":
    unittest.main()
