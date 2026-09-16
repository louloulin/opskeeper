from pathlib import Path
import unittest


class ReportingContractTest(unittest.TestCase):
    def test_worker_prompt_overrides_generic_task_file_lifecycle(self):
        prompt = (Path(__file__).parents[2] / "prompts" / "agent" / "worker.md").read_text()
        self.assertIn("不要创建 plan.md / result.md / spec.md", prompt)
        self.assertIn("不要调用 write_file", prompt)
        self.assertIn("直接在当前项目房间输出最终回报", prompt)

    def test_coordination_skill_forbids_file_artifact_dispatches(self):
        skill = (
            Path(__file__).parents[2]
            / "skills"
            / "team"
            / "opskeeper-coordination"
            / "SKILL.md"
        ).read_text()
        self.assertIn("禁止要求 Worker 创建 plan.md / result.md / spec.md", skill)
        self.assertIn("直接在当前项目房间回报", skill)

    def test_alerter_and_manager_contracts_use_direct_results(self):
        plugin_root = Path(__file__).parents[2]
        alerter = (
            plugin_root / "skills" / "agent" / "opskeeper-alerter" / "SKILL.md"
        ).read_text()
        manager = (plugin_root / "prompts" / "manager" / "AGENTS.md").read_text()
        self.assertIn("OPSKEEPER_RESULT <task_id>", alerter)
        self.assertIn("禁止创建 spec.md", alerter)
        self.assertIn("OPSKEEPER_RESULT <task_id>", manager)
        self.assertIn("禁止等待或要求", manager)

    def test_worker_contracts_do_not_require_unavailable_state_put(self):
        plugin_root = Path(__file__).parents[2]
        worker_prompt = (plugin_root / "prompts" / "agent" / "worker.md").read_text()
        coordination = (
            plugin_root / "skills" / "team" / "opskeeper-coordination" / "SKILL.md"
        ).read_text()
        repairer = (
            plugin_root / "skills" / "agent" / "opskeeper-repairer" / "SKILL.md"
        ).read_text()

        self.assertIn("不要调用 `state.put`", worker_prompt)
        self.assertIn("不得要求 `state.put`", coordination)
        self.assertIn("禁止调用 state.put", repairer)

    def test_plugin_manifest_omits_unavailable_state_tools(self):
        plugin_root = Path(__file__).parents[2]
        manifest = (plugin_root / "plugin.yaml").read_text()
        self.assertNotIn("- state.put", manifest)
        self.assertNotIn("- state.get", manifest)


if __name__ == "__main__":
    unittest.main()
