import asyncio
import importlib.util
import json
import sys
import unittest
import threading
from enum import Enum
from pathlib import Path
from types import ModuleType, SimpleNamespace
from typing import Any
from unittest.mock import patch


class _ToolResultState(str, Enum):
    DENIED = "denied"


class _TextBlock:
    def __init__(self, text: str):
        self.text = text


class _ToolResponse:
    def __init__(self, content: list[Any], state: _ToolResultState, metadata: dict[str, Any]):
        self.content = content
        self.state = state
        self.metadata = metadata


class _MiddlewareBase:
    def is_implemented(self, hook_name: str):
        return callable(getattr(self, hook_name, None))


def _install_agentscope_stubs() -> dict[str, Any]:
    agentscope = ModuleType("agentscope")
    middleware = ModuleType("agentscope.middleware")
    message = ModuleType("agentscope.message")
    tool = ModuleType("agentscope.tool")

    middleware.MiddlewareBase = _MiddlewareBase
    message.TextBlock = _TextBlock
    message.ToolResultState = _ToolResultState
    tool.ToolResponse = _ToolResponse
    agentscope.middleware = middleware
    agentscope.message = message
    agentscope.tool = tool

    installed = {
        "agentscope": agentscope,
        "agentscope.middleware": middleware,
        "agentscope.message": message,
        "agentscope.tool": tool,
    }
    saved = {name: sys.modules.get(name) for name in installed}
    sys.modules.update(installed)
    return saved


def _restore_agentscope(saved: dict[str, Any]) -> None:
    for name, module in saved.items():
        if module is None:
            sys.modules.pop(name, None)
        else:
            sys.modules[name] = module


def _load_plugin():
    path = Path(__file__).with_name("plugin.py")
    spec = importlib.util.spec_from_file_location("opskeeper_readonly_plugin_under_test", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load plugin.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ReadOnlyEnforcementTest(unittest.TestCase):
    def setUp(self):
        self.saved_modules = _install_agentscope_stubs()
        self.module = _load_plugin()

    def tearDown(self):
        _restore_agentscope(self.saved_modules)

    def _invoke(
        self,
        tool_name: str,
        permission_mode: str | None = None,
        arguments: dict | None = None,
        context: Any | None = None,
        environment: dict[str, str] | None = None,
    ):
        middleware = self.module._readonly_enforcement_factory(context, None)
        executed = False

        async def next_handler(**_kwargs):
            nonlocal executed
            executed = True
            yield "allowed"

        permission_environment = {} if permission_mode is None else {
            self.module._PERMISSION_MODE_ENV: permission_mode,
        }
        environment = {
            **(environment or {}),
            **permission_environment,
        }
        input_kwargs = {
            "tool_call": SimpleNamespace(
                name=tool_name,
                input=json.dumps(arguments or {}),
            ),
        }

        async def run():
            events = []
            async for event in middleware.on_acting(
                agent=None,
                input_kwargs=input_kwargs,
                next_handler=next_handler,
            ):
                events.append(event)
            return events

        with patch.dict("os.environ", environment, clear=False):
            events = asyncio.run(run())
        return events, executed

    def test_default_mode_is_read_only(self):
        self.assertEqual(self.module._permission_mode(), "read_only")

    def test_registered_middlewares_implement_the_agentscope_protocol(self):
        readonly = self.module._readonly_enforcement_factory(None, None)
        sanitizer = self.module._sanitizer_factory(None, None)
        outbound = self.module._outbound_safety_factory(None, None)
        for middleware in (readonly, sanitizer):
            self.assertTrue(middleware.is_implemented("on_acting"))
            self.assertFalse(middleware.is_implemented("on_reply"))
        self.assertTrue(outbound.is_implemented("on_acting"))
        self.assertTrue(outbound.is_implemented("on_reply"))

    def test_strip_thinking_removes_multiline_and_leading_partial_spans(self):
        self.assertEqual(
            self.module._strip_thinking(
                "<think>line one\nline two</think>\npublic result"
            ),
            "public result",
        )
        self.assertEqual(
            self.module._strip_thinking("<think>unfinished private reasoning"),
            "",
        )
        self.assertEqual(
            self.module._strip_thinking("<think>a</think><think>b"),
            "",
        )

    def test_sanitize_reply_event_handles_content_blocks_and_text_deltas(self):
        final_message = SimpleNamespace(
            content=[_TextBlock("<think>private</think>\npublic result")]
        )
        dict_content = SimpleNamespace(
            content=[{"type": "text", "text": "<think>private</think>public dict"}]
        )
        text_block = _TextBlock("<think>private</think>public block")
        delta = SimpleNamespace(text="<think>private</think>public delta")
        string_content = SimpleNamespace(
            content="<think>private</think>public content"
        )

        for event in (final_message, dict_content, text_block, delta, string_content):
            with self.subTest(event=event):
                self.assertIs(self.module._sanitize_reply_event(event), event)

        self.assertEqual(final_message.content[0].text, "public result")
        self.assertEqual(dict_content.content[0]["text"], "public dict")
        self.assertEqual(text_block.text, "public block")
        self.assertEqual(delta.text, "public delta")
        self.assertEqual(string_content.content, "public content")

    def test_reply_sanitizer_buffers_cross_chunk_thinking_deltas(self):
        middleware = self.module._outbound_safety_factory(None, None)
        chunks = ["<think>pri", "vate</think>public", " result"]

        async def next_handler(**_kwargs):
            for chunk in chunks:
                yield SimpleNamespace(text=chunk)

        async def invoke():
            return [
                event.text
                async for event in middleware.on_reply(
                    agent=None,
                    input_kwargs={},
                    next_handler=next_handler,
                )
            ]

        output = "".join(asyncio.run(invoke()))
        self.assertEqual(output, "public result")
        self.assertNotIn("private", output)
        self.assertNotIn("pri", output)
        self.assertNotIn("vate", output)

    def test_outbound_reply_is_sanitized_and_bounded_without_another_event(self):
        async def next_handler(**_kwargs):
            yield SimpleNamespace(text="<think>private</think>public result")

        async def invoke():
            return [
                event
                async for event in middleware.on_reply(
                    agent=None,
                    input_kwargs={},
                    next_handler=next_handler,
                )
            ]

        with patch.dict(
            "os.environ", {"OPSKEEPER_OUTBOUND_LIMIT": "1"}, clear=False
        ):
            middleware = self.module._outbound_safety_factory(None, None)
            events = asyncio.run(invoke())
            self.assertEqual(events[0].text, "public result")
            with self.assertRaisesRegex(
                RuntimeError, "OpsKeeper outbound rate limit exceeded"
            ):
                asyncio.run(invoke())

    def test_breached_reply_never_invokes_next_handler(self):
        middleware = self.module._outbound_safety_factory(None, None)
        calls = 0

        async def next_handler(**_kwargs):
            nonlocal calls
            calls += 1
            yield "reply"

        async def invoke():
            async for _event in middleware.on_reply(
                agent=None,
                input_kwargs={},
                next_handler=next_handler,
            ):
                pass

        with patch.dict(
            "os.environ", {"OPSKEEPER_OUTBOUND_LIMIT": "1"}, clear=False
        ):
            middleware = self.module._outbound_safety_factory(None, None)
            asyncio.run(invoke())
            with self.assertRaisesRegex(
                RuntimeError, "OpsKeeper outbound rate limit exceeded"
            ):
                asyncio.run(invoke())
        self.assertEqual(calls, 1)

    def test_reservation_clock_sampling_and_pruning_are_atomic(self):
        with patch.dict(
            "os.environ", {"OPSKEEPER_OUTBOUND_LIMIT": "8"}, clear=False
        ):
            middleware = self.module._outbound_safety_factory(None, None)
        barrier = threading.Barrier(16)
        evidence_lock = threading.Lock()
        active_calls = 0
        max_active_calls = 0
        errors: list[BaseException] = []

        def clock() -> float:
            nonlocal active_calls, max_active_calls
            with evidence_lock:
                active_calls += 1
                max_active_calls = max(max_active_calls, active_calls)
            threading.Event().wait(0.01)
            with evidence_lock:
                active_calls -= 1
            return 10.0

        def reserve():
            try:
                barrier.wait()
                middleware._reserve()
            except BaseException as exc:
                errors.append(exc)

        with patch.object(middleware, "_monotonic", clock):
            threads = [threading.Thread(target=reserve) for _ in range(16)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()

        self.assertEqual(max_active_calls, 1)
        self.assertEqual(len(errors), 8)
        self.assertTrue(
            all(isinstance(error, RuntimeError) for error in errors)
        )

    def test_duplicate_reservation_times_release_by_unique_token(self):
        with patch.dict(
            "os.environ", {"OPSKEEPER_OUTBOUND_LIMIT": "2"}, clear=False
        ):
            middleware = self.module._outbound_safety_factory(None, None)

        with patch.object(middleware, "_monotonic", return_value=7.0):
            first = middleware._reserve()
            second = middleware._reserve()
            middleware._release(second)
            middleware._reserve()
            with self.assertRaisesRegex(
                RuntimeError, "OpsKeeper outbound rate limit exceeded"
            ):
                middleware._reserve()

    def test_outbound_window_slides_and_invalid_settings_fall_back(self):
        with patch.dict(
            "os.environ",
            {
                "OPSKEEPER_OUTBOUND_LIMIT": "1001",
                "OPSKEEPER_OUTBOUND_WINDOW_SECONDS": "0",
            },
            clear=False,
        ):
            middleware = self.module._outbound_safety_factory(None, None)
        self.assertEqual(middleware._limit, 12)
        self.assertEqual(middleware._window_seconds, 10.0)

        with patch.dict(
            "os.environ",
            {
                "OPSKEEPER_OUTBOUND_LIMIT": "1",
                "OPSKEEPER_OUTBOUND_WINDOW_SECONDS": "1",
            },
            clear=False,
        ):
            middleware = self.module._outbound_safety_factory(None, None)

        async def next_handler(**_kwargs):
            yield "reply"

        async def invoke():
            async for _event in middleware.on_reply(
                agent=None,
                input_kwargs={},
                next_handler=next_handler,
            ):
                pass

        with patch.object(middleware, "_monotonic", side_effect=[0.0, 2.0]):
            asyncio.run(invoke())
            asyncio.run(invoke())

    def test_successful_message_attempts_share_outbound_boundary(self):
        with patch.dict(
            "os.environ", {"OPSKEEPER_OUTBOUND_LIMIT": "1"}, clear=False
        ):
            middleware = self.module._outbound_safety_factory(None, None)
        input_kwargs = {
            "tool_call": SimpleNamespace(
                name="teamharness__message",
                input=json.dumps({"message": "sent"}),
            ),
        }

        async def next_handler(**_kwargs):
            yield _ToolResponse(
                content=[_TextBlock("sent")],
                state="success",
                metadata={},
            )

        async def invoke(tool_name="teamharness__message"):
            kwargs = {
                **input_kwargs,
                "tool_call": SimpleNamespace(
                    name=tool_name,
                    input=json.dumps({"message": "sent"}),
                ),
            }
            async for _event in middleware.on_acting(
                agent=None,
                input_kwargs=kwargs,
                next_handler=next_handler,
            ):
                pass

        asyncio.run(invoke())
        with self.assertRaisesRegex(
            RuntimeError, "OpsKeeper outbound rate limit exceeded"
        ):
            asyncio.run(invoke())

    def test_failed_message_attempt_does_not_consume_outbound_boundary(self):
        with patch.dict(
            "os.environ", {"OPSKEEPER_OUTBOUND_LIMIT": "1"}, clear=False
        ):
            middleware = self.module._outbound_safety_factory(None, None)
        input_kwargs = {
            "tool_call": SimpleNamespace(
                name="teamharness__message",
                input=json.dumps({"message": "not sent"}),
            ),
        }

        async def failed_handler(**_kwargs):
            yield _ToolResponse(
                content=[],
                state=_ToolResultState.DENIED,
                metadata={},
            )

        async def successful_handler(**_kwargs):
            yield _ToolResponse(
                content=[_TextBlock("sent")],
                state="success",
                metadata={},
            )

        async def invoke(next_handler):
            async for _event in middleware.on_acting(
                agent=None,
                input_kwargs=input_kwargs,
                next_handler=next_handler,
            ):
                pass

        asyncio.run(invoke(failed_handler))
        asyncio.run(invoke(successful_handler))

    def test_message_exception_after_nonterminal_event_releases_reservation(self):
        with patch.dict(
            "os.environ", {"OPSKEEPER_OUTBOUND_LIMIT": "1"}, clear=False
        ):
            middleware = self.module._outbound_safety_factory(None, None)
        input_kwargs = {
            "tool_call": SimpleNamespace(
                name="teamharness__message",
                input=json.dumps({"message": "maybe sent"}),
            ),
        }

        async def failing_handler(**_kwargs):
            yield "progress"
            raise RuntimeError("transport failed")

        async def successful_handler(**_kwargs):
            yield _ToolResponse(
                content=[_TextBlock("sent")],
                state="success",
                metadata={},
            )

        async def invoke(next_handler):
            async for _event in middleware.on_acting(
                agent=None,
                input_kwargs=input_kwargs,
                next_handler=next_handler,
            ):
                pass

        with self.assertRaisesRegex(RuntimeError, "transport failed"):
            asyncio.run(invoke(failing_handler))
        asyncio.run(invoke(successful_handler))

    def test_denial_after_success_releases_message_reservation(self):
        with patch.dict(
            "os.environ", {"OPSKEEPER_OUTBOUND_LIMIT": "1"}, clear=False
        ):
            middleware = self.module._outbound_safety_factory(None, None)
        input_kwargs = {
            "tool_call": SimpleNamespace(
                name="teamharness__message",
                input=json.dumps({"message": "ambiguous outcome"}),
            ),
        }

        async def denied_after_success(**_kwargs):
            yield _ToolResponse(
                content=[_TextBlock("transient")],
                state="success",
                metadata={},
            )
            yield _ToolResponse(
                content=[],
                state=_ToolResultState.DENIED,
                metadata={},
            )

        async def successful_handler(**_kwargs):
            yield _ToolResponse(
                content=[_TextBlock("sent")],
                state="success",
                metadata={},
            )

        async def invoke(next_handler):
            async for _event in middleware.on_acting(
                agent=None,
                input_kwargs=input_kwargs,
                next_handler=next_handler,
            ):
                pass

        asyncio.run(invoke(denied_after_success))
        asyncio.run(invoke(successful_handler))

    def test_write_file_is_denied_without_execution(self):
        events, executed = self._invoke("write_file")
        self.assertFalse(executed)
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0].state, _ToolResultState.DENIED)
        self.assertIn("write_file", events[0].content[0].text)

    def test_worker_message_to_synthetic_manager_room_is_denied_before_execution(self):
        context = SimpleNamespace(session_id="matrix:!project-room:matrix.local")
        arguments = {
            "action": "send",
            "channel": "matrix",
            "target": "room:!manager:matrix.local",
            "message": "@manager:matrix.local OPSKEEPER_RESULT task-001 {}",
        }
        events, executed = self._invoke(
            "teamharness__message",
            arguments=arguments,
            context=context,
            environment={"AGENTTEAMS_AGENT_NAME": "investigator"},
        )
        self.assertFalse(executed)
        self.assertEqual(events[0].state, _ToolResultState.DENIED)
        self.assertIn("current project room", events[0].content[0].text)

    def test_manager_dispatch_requiring_file_artifacts_is_denied(self):
        context = SimpleNamespace(session_id="matrix:!manager-room:matrix.local")
        arguments = {
            "action": "send",
            "channel": "matrix",
            "target": "room:!project-room:matrix.local",
            "message": (
                "@investigator:matrix.local OPSKEEPER TASK task-001\n"
                "请创建 plan.md 与 result.md。"
            ),
        }
        events, executed = self._invoke(
            "teamharness__message",
            arguments=arguments,
            context=context,
            environment={"AGENTTEAMS_AGENT_NAME": "manager"},
        )
        self.assertFalse(executed)
        self.assertEqual(events[0].state, _ToolResultState.DENIED)
        self.assertIn("must not require plan.md, result.md, or spec.md", events[0].content[0].text)

    def test_manager_dispatch_requiring_spec_artifact_is_denied(self):
        context = SimpleNamespace(session_id="matrix:!manager-room:matrix.local")
        arguments = {
            "action": "send",
            "channel": "matrix",
            "target": "room:!project-room:matrix.local",
            "message": (
                "@alerter:matrix.local OPSKEEPER TASK task-001\n"
                "完成后写入 shared/tasks/incident-1/spec.md。"
            ),
        }
        events, executed = self._invoke(
            "teamharness__message",
            arguments=arguments,
            context=context,
            environment={"AGENTTEAMS_AGENT_NAME": "manager"},
        )
        self.assertFalse(executed)
        self.assertEqual(events[0].state, _ToolResultState.DENIED)
        self.assertIn("must not require plan.md, result.md, or spec.md", events[0].content[0].text)

    def test_negated_file_artifact_instruction_is_allowed(self):
        context = SimpleNamespace(session_id="matrix:!manager-room:matrix.local")
        arguments = {
            "action": "send",
            "channel": "matrix",
            "target": "room:!project-room:matrix.local",
            "message": (
                "@investigator:matrix.local OPSKEEPER TASK task-001\n"
                "不要创建 plan.md / result.md / spec.md；直接在当前项目房间回报。"
            ),
        }
        events, executed = self._invoke(
            "teamharness__message",
            arguments=arguments,
            context=context,
            environment={"AGENTTEAMS_AGENT_NAME": "manager"},
        )
        self.assertTrue(executed)
        self.assertEqual(events, ["allowed"])

    def test_repeated_denied_tool_terminates_after_boundary(self):
        context = SimpleNamespace(session_id="matrix:!project-room:matrix.local")
        middleware = self.module._readonly_enforcement_factory(context, None)
        input_kwargs = {
            "tool_call": SimpleNamespace(name="write_file", input=json.dumps({"path": "result.md"})),
        }

        async def next_handler(**_kwargs):
            yield "allowed"

        async def invoke():
            return [
                event
                async for event in middleware.on_acting(
                    agent=None,
                    input_kwargs=input_kwargs,
                    next_handler=next_handler,
                )
            ]

        with self.assertRaisesRegex(RuntimeError, "repeated read-only denials"):
            for _ in range(3):
                asyncio.run(invoke())

    def test_prefixed_mutating_opskeeper_tool_is_denied(self):
        events, executed = self._invoke("opskeeper__state_put")
        self.assertFalse(executed)
        self.assertEqual(events[0].state, _ToolResultState.DENIED)

    def test_task_coordination_state_put_is_allowed_in_read_only_mode(self):
        events, executed = self._invoke(
            "opskeeper__task_state_put",
            arguments={"task_id": "task-001", "state": "acknowledged"},
        )
        self.assertTrue(executed)
        self.assertEqual(events, ["allowed"])

    def test_recovery_execute_requires_a_complete_proposal_binding(self):
        arguments = {
            "incident_id": "incident-live-pool",
            "proposal_id": "13a1c286-ca32-483c-bffd-647b65e313d0",
            "skill_id": "resize-pg-pool",
            "target": "pg:pool-fixture",
            "resource_type": "pg",
            "parameters": {
                "command": "resize_pool",
                "incident_id": "incident-live-pool",
                "pool_manifest_id": "7f5c60e593e68840f974789166cc3374",
                "reason": "Resize the disposable fixture pool.",
            },
        }
        partial = dict(arguments)
        del partial["proposal_id"]
        self.assertFalse(self.module._is_proposal_bound_recovery_execute(
            "opskeeper.recovery.execute", partial,
        ))
        events, executed = self._invoke("opskeeper__recovery_execute", arguments=partial)
        self.assertFalse(executed)
        self.assertEqual(events[0].state, _ToolResultState.DENIED)

        events, executed = self._invoke("opskeeper__recovery_execute", arguments=arguments)
        self.assertTrue(executed)
        self.assertEqual(events, ["allowed"])

    def test_recovery_execute_rejects_skip_audit_and_unbound_pool_manifest(self):
        arguments = {
            "incident_id": "incident-live-pool",
            "proposal_id": "13a1c286-ca32-483c-bffd-647b65e313d0",
            "skill_id": "resize-pg-pool",
            "target": "pg:pool-fixture",
            "resource_type": "pg",
            "parameters": {
                "command": "resize_pool",
                "incident_id": "incident-live-pool",
                "pool_manifest_id": "7f5c60e593e68840f974789166cc3374",
                "reason": "Resize the disposable fixture pool.",
            },
        }
        for mutation in (
            {"parameters": {**arguments["parameters"], "skip_audit": True}},
            {"parameters": {**arguments["parameters"], "pool_manifest_id": ""}},
            {"parameters": {**arguments["parameters"], "incident_id": "another-incident"}},
        ):
            with self.subTest(mutation=mutation):
                invalid = {**arguments, **mutation}
                self.assertFalse(self.module._is_proposal_bound_recovery_execute(
                    "opskeeper.recovery.execute", invalid,
                ))
                events, executed = self._invoke("opskeeper__recovery_execute", arguments=invalid)
                self.assertFalse(executed)
                self.assertEqual(events[0].state, _ToolResultState.DENIED)

    def test_incident_record_is_an_append_only_read_only_exception(self):
        events, executed = self._invoke("opskeeper__incident_record")
        self.assertTrue(executed)
        self.assertEqual(events, ["allowed"])

    def test_reporter_knowledge_write_is_role_gated_by_backend(self):
        for tool_name in ("opskeeper__knowledge_write", "opskeeper__incident_record"):
            with self.subTest(tool_name=tool_name):
                events, executed = self._invoke(tool_name, arguments={})
                self.assertTrue(executed)
                self.assertEqual(events, ["allowed"])

    def test_shell_and_browser_are_denied(self):
        for tool_name in ("execute_shell_command", "browser_use"):
            with self.subTest(tool_name=tool_name):
                events, executed = self._invoke(tool_name)
                self.assertFalse(executed)
                self.assertEqual(events[0].state, _ToolResultState.DENIED)

    def test_readonly_tools_are_allowed(self):
        for tool_name in (
            "message",
            "teamharness__message",
            "read_file",
            "opskeeper__metric_query",
            "opskeeper__postgres_analyze_status",
            "opskeeper__query_promql",
            "opskeeper__query_incidents",
            "opskeeper__get_incident_detail",
            "opskeeper__analyze_database_status",
            "opskeeper__query_knowledge",
        ):
            with self.subTest(tool_name=tool_name):
                events, executed = self._invoke(tool_name)
                self.assertTrue(executed)
                self.assertEqual(events, ["allowed"])

    def test_standard_mode_explicitly_allows_mutation(self):
        events, executed = self._invoke("write_file", permission_mode="standard")
        self.assertTrue(executed)
        self.assertEqual(events, ["allowed"])

    def test_invalid_permission_mode_fails_closed(self):
        events, executed = self._invoke("edit_file", permission_mode="write-anything")
        self.assertFalse(executed)
        self.assertEqual(events[0].state, _ToolResultState.DENIED)

    def test_enforcement_is_registered_outside_sanitizer_and_audit(self):
        registrations = []

        class FakeApi:
            def register_prompt_section(self, *args, **kwargs):
                pass

            def register_skill_provider(self, *args, **kwargs):
                pass

            def register_middleware(self, factory, priority):
                registrations.append((factory, priority))

            def register_runtime_hook(self, *args, **kwargs):
                pass

        with patch.object(self.module, "_load_task_trace_module", return_value=None):
            self.module.plugin.register(FakeApi())
        priorities = {factory: priority for factory, priority in registrations}
        self.assertEqual(
            priorities[self.module._readonly_enforcement_factory],
            10,
        )
        self.assertLess(
            priorities[self.module._readonly_enforcement_factory],
            priorities[self.module._outbound_safety_factory],
        )
        self.assertLess(
            priorities[self.module._outbound_safety_factory],
            priorities[self.module._sanitizer_factory],
        )

    def test_copaw_toolkit_registers_outbound_between_readonly_and_sanitizer(self):
        registered = []

        class FakeToolkit:
            def register_middleware(self, middleware):
                registered.append(middleware)

            def register_tool_function(self, *_args, **_kwargs):
                pass

        def middleware(label: str):
            async def on_acting(
                _agent, _input_kwargs, next_handler
            ):
                labels.append(label)
                async for event in next_handler():
                    yield event

            return SimpleNamespace(on_acting=on_acting)

        labels = []

        def copaw_next_handler(**_kwargs):
            async def events():
                yield "raw"

            return events()

        async def invoke_registered_middleware():
            output = []
            for middleware in registered:
                async for event in middleware.on_acting(
                    None,
                    {},
                    lambda **kwargs: copaw_next_handler(),
                ):
                    output.append(event)
            return output

        all_tools = (
            self.module._COPAW_BASE_TOOLS
            | set(self.module._COPAW_NATIVE_TOOLS)
        )
        with patch.object(
            self.module,
            "_readonly_enforcement_factory",
            lambda *_args: middleware("readonly"),
        ), patch.object(
            self.module,
            "_outbound_safety_factory",
            lambda *_args: middleware("outbound"),
        ), patch.object(
            self.module,
            "_sanitizer_factory",
            lambda *_args: middleware("sanitizer"),
        ), patch.object(
            self.module,
            "_as_copaw_toolkit_middleware",
            lambda item: item,
        ), patch.object(
            self.module,
            "_tool_names",
            lambda _toolkit: all_tools,
        ), patch.object(
            self.module,
            "_call_opskeeper_mcp_server",
            lambda *_args: {},
        ):
            self.module._validate_copaw_toolkit(FakeToolkit())

        self.assertEqual(
            asyncio.run(invoke_registered_middleware()),
            ["raw", "raw", "raw"],
        )
        self.assertEqual(labels, ["readonly", "outbound", "sanitizer"])

    def test_qwenpaw_outbound_registration_failure_fails_closed(self):
        outbound_factory = self.module._outbound_safety_factory

        class FailingApi:
            def register_prompt_section(self, *_args, **_kwargs):
                pass

            def register_skill_provider(self, *_args, **_kwargs):
                pass

            def register_middleware(self, factory, priority):
                if factory is outbound_factory:
                    raise RuntimeError("middleware registry rejected safety")

            def register_runtime_hook(self, *_args, **_kwargs):
                pass

        with patch.object(self.module, "_load_task_trace_module", return_value=None):
            with self.assertRaisesRegex(RuntimeError, "middleware registry rejected"):
                self.module.plugin.register(FailingApi())


if __name__ == "__main__":
    unittest.main(verbosity=2)
