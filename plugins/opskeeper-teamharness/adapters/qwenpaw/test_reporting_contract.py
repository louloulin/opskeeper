from pathlib import Path
import unittest


class ReportingContractTest(unittest.TestCase):
    def test_worker_prompt_overrides_generic_task_file_lifecycle(self):
        prompt = (Path(__file__).parents[2] / "prompts" / "agent" / "worker.md").read_text()
        self.assertIn("不要创建 plan.md / result.md", prompt)
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
        self.assertIn("禁止要求 Worker 创建 plan.md / result.md", skill)
        self.assertIn("直接在当前项目房间回报", skill)


if __name__ == "__main__":
    unittest.main()
