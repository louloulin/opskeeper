import asyncio
import importlib.util
import sys
import types
import unittest
from pathlib import Path


class _MiddlewareBase:
    pass


class _TextBlock:
    def __init__(self, text):
        self.text = text


class _ToolResultState:
    DENIED = "denied"


class _ToolResponse:
    def __init__(self, content, state, metadata):
        self.content = content
        self.state = state
        self.metadata = metadata


class CoPawCompatTest(unittest.TestCase):
    def setUp(self):
        agentscope = types.ModuleType("agentscope")
        middleware = types.ModuleType("agentscope.middleware")
        message = types.ModuleType("agentscope.message")
        tool = types.ModuleType("agentscope.tool")
        middleware.MiddlewareBase = _MiddlewareBase
        message.TextBlock = _TextBlock
        message.ToolResultState = _ToolResultState
        tool.ToolResponse = _ToolResponse
        installed = {
            "agentscope": agentscope,
            "agentscope.middleware": middleware,
            "agentscope.message": message,
            "agentscope.tool": tool,
        }
        self.saved_modules = {name: sys.modules.get(name) for name in installed}
        sys.modules.update(installed)
        path = Path(__file__).with_name("plugin.py")
        spec = importlib.util.spec_from_file_location("opskeeper_copaw_plugin_under_test", path)
        self.module = importlib.util.module_from_spec(spec)
        sys.modules["opskeeper_copaw_plugin_under_test"] = self.module
        spec.loader.exec_module(self.module)

        self.saved_copaw = tuple(sys.modules.pop(name, None) for name in ("copaw", "copaw.agents", "copaw.agents.react_agent"))
        self.original_create_toolkit = staticmethod(lambda agent: None)
        react_agent = types.ModuleType("copaw.agents.react_agent")
        react_agent.CoPawAgent = self._agent_class()
        agents_module = types.ModuleType("copaw.agents")
        agents_module.react_agent = react_agent
        copaw = types.ModuleType("copaw")
        copaw.agents = agents_module
        sys.modules.update({"copaw": copaw, "copaw.agents": agents_module, "copaw.agents.react_agent": react_agent})

    def tearDown(self):
        for name, module in zip(("copaw", "copaw.agents", "copaw.agents.react_agent"), self.saved_copaw):
            if module is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = module
        for name, module in self.saved_modules.items():
            if module is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = module

    def _agent_class(self):
        test = self

        class Toolkit:
            def __init__(self):
                self.tools = {name: object() for name in ("message", "filesync", "projectflow", "taskflow")}
                self.middlewares = []

            def register_middleware(self, factory):
                self.middlewares.append(factory)

            def register_tool_function(self, tool_func, group_name="basic", **kwargs):
                self.tools[kwargs["func_name"]] = tool_func

        class CoPawAgent:
            _create_toolkit = staticmethod(lambda agent: test.toolkit_class())

        self.toolkit_class = Toolkit
        return CoPawAgent

    def _api(self, **methods):
        class API:
            def __init__(self):
                self.startup_hooks = []
                for name, value in methods.items():
                    setattr(self, name, value)

            def register_provider(self, provider_id, provider_class, *args, **kwargs):
                raise AssertionError("diagnostics must not be registered as an LLM provider")

            def register_startup_hook(self, hook_name, callback, priority=100):
                self.startup_hooks.append((hook_name, callback, priority))

        defaults = {
            "register_shutdown_hook": lambda *args, **kwargs: None,
            "register_control_command": lambda *args, **kwargs: None,
        }
        defaults.update(methods)
        methods = defaults
        return API()

    def test_full_copaw_api_installs_idempotent_toolkit_hook(self):
        api = self._api()
        diagnostics = self.module.plugin.register(api)
        self.assertTrue(diagnostics["wrap_installed"])
        self.assertFalse(diagnostics["toolkit_validated"])
        toolkit = sys.modules["copaw.agents.react_agent"].CoPawAgent._create_toolkit(object())
        self.assertNotEqual(toolkit.middlewares[0], self.module._readonly_enforcement_factory)
        self.assertNotEqual(toolkit.middlewares[1], self.module._sanitizer_factory)
        self.assertIn("opskeeper__state_get", toolkit.tools)
        self.assertIn("message", toolkit.tools)
        self.assertEqual(len(api.startup_hooks), 1)
        self.assertEqual(api.startup_hooks[0][0], "opskeeper_teamharness_diagnostics")
        self.assertEqual(api.startup_hooks[0][2], 0)

        second = self.module.plugin.register(api)
        self.assertTrue(second["wrap_installed"])
        self.assertTrue(second["toolkit_validated"])
        self.assertEqual(second["native_tool_count"], 6)
        self.assertEqual(second["missing_capabilities"], [])
        self.assertIn("signature_hash", second)
        self.assertEqual(api.startup_hooks[0][1](), second)

    def test_copaw_direct_middleware_intercepts_before_tool_execution(self):
        self.module.plugin.register(self._api())
        toolkit = sys.modules["copaw.agents.react_agent"].CoPawAgent._create_toolkit(object())
        readonly_middleware = toolkit.middlewares[0]
        input_kwargs = {
            "tool_call": type("ToolCall", (), {
                "name": "write_file",
                "input": "{\"path\":\"result.md\"}",
            })(),
        }
        executed = []

        async def next_handler(**kwargs):
            async def events():
                executed.append(kwargs)
                yield {"continued": True}

            return events()

        async def invoke():
            stream = readonly_middleware(input_kwargs, next_handler)
            return [event async for event in stream]

        events = asyncio.run(invoke())
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].state, "denied")
        self.assertEqual(executed, [])

    def test_partial_copaw_api_hard_fails(self):
        api = self._api(register_startup_hook=lambda *args, **kwargs: None)
        del api.register_control_command
        with self.assertRaisesRegex(RuntimeError, "missing.*register_control_command"):
            self.module.plugin.register(api)

    def test_import_failure_hard_fails(self):
        sys.modules["copaw.agents.react_agent"] = None
        api = self._api()
        with self.assertRaisesRegex(RuntimeError, "cannot import CoPawAgent"):
            self.module.plugin.register(api)

    def test_first_toolkit_registration_failure_hard_fails(self):
        api = self._api()
        self.module.plugin.register(api)
        original_method = self.toolkit_class.register_middleware

        def register_middleware(_self, factory):
            raise RuntimeError("registration rejected")

        self.toolkit_class.register_middleware = register_middleware
        try:
            with self.assertRaisesRegex(RuntimeError, "registration rejected"):
                sys.modules["copaw.agents.react_agent"].CoPawAgent._create_toolkit(object())
        finally:
            self.toolkit_class.register_middleware = original_method
        diagnostics = self.module._copaw_diagnostics()
        self.assertFalse(diagnostics["toolkit_validated"])
        self.assertTrue(diagnostics["missing_capabilities"])

    def test_readonly_rejects_write_and_shell(self):
        self.module.plugin.register(self._api())
        middleware = self.module._readonly_enforcement_factory(None, None)
        executed = []

        async def next_handler(**kwargs):
            executed.append(kwargs)
            yield {"continued": True}

        async def invoke(tool_name):
            input_kwargs = {"tool_call": type("ToolCall", (), {"name": tool_name, "input": "{}"})()}
            return [event async for event in middleware.on_acting(None, input_kwargs, next_handler)]

        for tool_name in ("write_file", "execute_command"):
            with self.subTest(tool_name=tool_name):
                events = asyncio.run(invoke(tool_name))
                self.assertEqual(events[0].state, "denied")
                self.assertEqual(executed, [])

    def test_manager_gate_and_result_contract(self):
        self.module.plugin.register(self._api())
        marker = "OPSKEEPER-TASK-123"
        self.assertTrue(self.module._has_new_task(f"dispatch {marker} now"))
        self.module._MANAGER_DISPATCH_GATE.record_request_origin("origin", f"dispatch {marker}")
        self.module._MANAGER_DISPATCH_GATE.record("session", f"dispatch {marker}")
        self.assertEqual(set(self.module._MANAGER_DISPATCH_GATE.pending_markers("session")), {marker})
        consumed = self.module._MANAGER_DISPATCH_GATE.consume_result_with_origins("session", f"OPSKEEPER_RESULT {marker}")
        self.assertEqual(consumed, {marker: "origin"})
        self.assertEqual(self.module._MANAGER_DISPATCH_GATE.pending_markers("session"), ())


if __name__ == "__main__":
    unittest.main()
