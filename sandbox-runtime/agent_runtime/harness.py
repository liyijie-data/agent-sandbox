from __future__ import annotations

import hashlib
import json
import os
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Any, Callable, Dict, List, Optional

from . import constants as C
from . import result as result_mod
from .adapters import CapabilityNotImplemented
from .artifacts import ArtifactBundler, ArtifactDeliveryError
from .model import ModelBackend, ModelError
from .recovery import PackageError, build_package, restore_package
from .steering import (
    SafeNodeCoordinator,
    SteeringCheckpointMismatch,
    SteeringCursorConflict,
    SteeringError,
)
from .tools.builtin import build_builtin_tools

REFERENCE_STATE_FORMAT = "agent-runtime-reference/1"
MAX_ITERATIONS = 40

ToolRunner = Callable[[str, str], Dict[str, Any]]


class CancelRequested(Exception):
    pass


class ExecutionError(Exception):

    def __init__(self, code: str, err_type: str = "AgentExecutionError"):
        super().__init__(code)
        self.code = code
        self.err_type = err_type


def _serialize_state(context: List[Dict[str, Any]], cursor: int) -> bytes:
    return json.dumps({"messages": context, "cursor": cursor},
                      ensure_ascii=False).encode("utf-8")


def _deserialize_state(data: bytes) -> Dict[str, Any]:
    return json.loads(data.decode("utf-8"))


def _dir_hashes(directory: str) -> Dict[str, str]:
    out: Dict[str, str] = {}
    if not os.path.isdir(directory):
        return out
    for root, _dirs, files in sorted(os.walk(directory)):
        for fn in sorted(files):
            full = os.path.join(root, fn)
            rel = os.path.relpath(full, directory).replace(os.sep, "/")
            with open(full, "rb") as fh:
                out[rel] = hashlib.sha256(fh.read()).hexdigest()
    return out


class RuntimeCoordinator:
    def __init__(
        self,
        config: Dict[str, Any],
        model: ModelBackend,
        steering: Optional[SafeNodeCoordinator] = None,
        tools: Optional[Dict[str, ToolRunner]] = None,
        request_input_policy: Optional[Callable[[List[Dict[str, Any]]], Optional[Dict[str, Any]]]] = None,
        cancel_event: Optional[threading.Event] = None,
        workspace_dir: str = C.WORKSPACE_DIR,
        output_dir: str = C.OUTPUT_DIR,
        checkpoint_path: str = C.CHECKPOINT_PATH,
        resume_hook: Optional[Callable[[], None]] = None,
        attach_context: Optional[List[Dict[str, Any]]] = None,
        builtin_prompt: Optional[str] = None,
        unavailable_tools: Optional[List[Dict[str, str]]] = None,
        artifact_bundler: Optional[ArtifactBundler] = None,
    ):
        self.config = config
        self.model = model
        self.steering = steering
        self.request_input_policy = request_input_policy
        self.cancel_event = cancel_event or threading.Event()
        self.workspace_dir = workspace_dir
        self.output_dir = output_dir
        self.checkpoint_path = checkpoint_path
        self.tools = dict(tools or {})
        self.tools.update(build_builtin_tools(
            workspace_dir, output_dir,
            cancel_event=self.cancel_event,
            remaining_seconds_provider=lambda: float(self._remaining_seconds())))
        self.builtin_prompt = builtin_prompt
        self.unavailable_tools = list(unavailable_tools or [])
        self.artifact_bundler = artifact_bundler
        self.attach_context = list(attach_context or [])
        self.resume_hook = resume_hook

        self.limits = config.get("limits") or {}
        self.budget = int(self.limits.get(
            "remaining_execution_seconds", C.DEFAULT_REMAINING_EXECUTION_SECONDS))
        self.started = time.monotonic()

        self.cursor = int((config.get("steering") or {}).get("after_seq", 0))
        if steering is not None:
            steering.set_cursor(self.cursor)

        self.baseline: Dict[str, Any] = {}

    def _cancel_check(self) -> None:
        if self.cancel_event.is_set():
            raise CancelRequested()

    def _budget_left(self) -> float:
        return self.budget - (time.monotonic() - self.started)

    def _remaining_seconds(self) -> float:
        return max(0.0, self._budget_left())

    def _restore_resume_context(self) -> None:
        resume = self.config.get("resume")
        if not resume:
            raise ExecutionError(C.ERR_RESUME_FAILED,
                                 "resume config missing on resumed stage")
        cp_path = resume["checkpoint_path"]
        if not os.path.exists(cp_path):
            raise PackageError(f"checkpoint missing: {cp_path}")
        restored = restore_package(
            cp_path, self.workspace_dir, self.output_dir, C.APP_ROOT,
            max_bytes=int(self.limits.get("checkpoint_max_bytes",
                                          C.DEFAULT_CHECKPOINT_MAX_BYTES)))
        if restored.state_format != REFERENCE_STATE_FORMAT:
            raise PackageError(
                f"state_format mismatch: {restored.state_format!r} != {REFERENCE_STATE_FORMAT!r}")

        state = _deserialize_state(restored.state_bytes)
        context = state.get("messages", [])
        restored_cursor = int(state.get("cursor", restored.steering_cursor))

        seed_cursor = int((self.config.get("steering") or {}).get("after_seq", 0))
        if seed_cursor != restored_cursor or restored.steering_cursor != restored_cursor:
            raise SteeringCheckpointMismatch(
                f"resume cursor mismatch: run={seed_cursor} "
                f"pkg={restored.steering_cursor} state={restored_cursor}")
        self.cursor = restored_cursor
        if self.steering is not None:
            self.steering.set_cursor(restored_cursor)

        self.baseline = restored.baseline or {}

        if self.resume_hook is not None:
            self.resume_hook()

        answer = resume.get("answer")
        if answer is not None:
            context.append({"role": C.ROLE_USER, "content": answer})
        self._context = context

    def _initial_context(self) -> List[Dict[str, Any]]:
        if self.config.get("resume"):
            self._restore_resume_context()
            return self._context
        self.baseline = {"workspace": _dir_hashes(self.workspace_dir)}
        messages = list(self.config.get("messages") or [])
        if self.builtin_prompt:
            messages = [{"role": C.ROLE_SYSTEM, "content": self.builtin_prompt}] + messages
        if self.unavailable_tools:
            messages = messages + [{"role": C.ROLE_SYSTEM,
                                    "content": _unavailable_tools_notice(self.unavailable_tools)}]
        return messages

    def _pause(self, context: List[Dict[str, Any]], kind: str, prompt: str,
               options: Optional[List[Dict[str, Any]]] = None) -> Dict[str, Any]:
        state_bytes = _serialize_state(context, self.cursor)
        steering_json = {"incorporated_through_seq": self.cursor}
        manifest = build_package(
            self.workspace_dir, self.output_dir, state_bytes,
            self.baseline, steering_json, self.checkpoint_path,
            contract_version=self.config.get("contract_version", C.CONTRACT_VERSION),
            run_id=self.config.get("run_id", ""),
            stage=int(self.config.get("stage", 1)),
            fence=int(self.config.get("fence", 0)),
            state_format=REFERENCE_STATE_FORMAT,
            image_digest="",
            max_bytes=int(self.limits.get("checkpoint_max_bytes",
                                          C.DEFAULT_CHECKPOINT_MAX_BYTES)),
        )
        with open(self.checkpoint_path, "rb") as checkpoint_file:
            sha = hashlib.sha256(checkpoint_file.read()).hexdigest()
        size = os.path.getsize(self.checkpoint_path)
        return result_mod.awaiting_input_result(
            kind, prompt, options or [], self.checkpoint_path,
            manifest["state_format"], sha, size, cursor=self.cursor)

    def _ok_result(self, summary: str) -> Dict[str, Any]:
        if self.artifact_bundler is None:
            return result_mod.ok_result(summary, cursor=self.cursor)
        try:
            bundle = self.artifact_bundler.collect(
                self.baseline, self.workspace_dir, self.output_dir)
        except ArtifactDeliveryError as exc:
            raise ExecutionError(exc.code, type(exc).__name__)
        if bundle is None:
            dest = self.artifact_bundler.destination_id
            if dest:
                return result_mod.ok_result(
                    summary, cursor=self.cursor,
                    delivery_status=C.DELIVERY_EMPTY, destination_id=dest)
            return result_mod.ok_result(summary, cursor=self.cursor)
        try:
            self.artifact_bundler.upload(bundle)
        except ArtifactDeliveryError as exc:
            raise ExecutionError(exc.code, type(exc).__name__)
        return result_mod.ok_result(
            summary, cursor=self.cursor,
            delivery_status=C.DELIVERY_UPLOADED,
            destination_id=self.artifact_bundler.destination_id,
            sha256=bundle.sha256, size_bytes=bundle.size_bytes)

    MODEL_RETRY_LIMIT = 2
    MODEL_RETRY_DELAYS = (1.0, 2.0)

    def _chat_with_retry(self, context: List[Dict[str, Any]]) -> Dict[str, Any]:
        attempt = 0
        while True:
            try:
                return self.model.chat(context)
            except ModelError as exc:
                if not getattr(exc, "retryable", False) or \
                        attempt >= self.MODEL_RETRY_LIMIT:
                    raise
                delay = self.MODEL_RETRY_DELAYS[min(attempt, len(self.MODEL_RETRY_DELAYS) - 1)]
                attempt += 1
                deadline = time.monotonic() + delay
                while time.monotonic() < deadline:
                    self._cancel_check()
                    if self._budget_left() <= 0:
                        raise
                    time.sleep(min(0.1, deadline - time.monotonic()))

    def run(self) -> Dict[str, Any]:
        context = self._initial_context()
        if self.attach_context:
            context = context + self.attach_context
        for _ in range(MAX_ITERATIONS):
            self._cancel_check()
            if self._budget_left() <= 0:
                raise ExecutionError(C.ERR_EXECUTION_TIMEOUT, "ExecutionTimeout")

            if self.steering is not None:
                try:
                    self.steering.before_model(context)
                except SteeringCheckpointMismatch:
                    raise
                except SteeringError as exc:
                    raise ExecutionError(exc.code, type(exc).__name__)
                self.cursor = self.steering.confirmed_cursor

            try:
                resp = self._chat_with_retry(context)
            except ModelError as exc:
                raise ExecutionError(C.ERR_AGENT_EXECUTION_FAILED, type(exc).__name__)
            msg = resp.get("message") or {}

            assistant: Dict[str, Any] = {"role": C.ROLE_ASSISTANT}
            content = msg.get("content")
            if content is not None:
                assistant["content"] = content
            tool_calls = msg.get("tool_calls") or []
            if tool_calls:
                assistant["tool_calls"] = tool_calls
            context.append(assistant)

            if tool_calls:
                self._report_tool_calls(tool_calls)

            if self.request_input_policy is not None:
                req = self.request_input_policy(context)
                if req:
                    tool_id = req.get("tool_call_id")
                    if tool_id:
                        if len(tool_calls) != 1:
                            raise ExecutionError(C.ERR_RUNTIME_PROTOCOL_INVALID,
                                                 "request_input cannot be mixed with other tool calls")
                        context.append({
                            "role": C.ROLE_TOOL,
                            "tool_call_id": tool_id,
                            "content": json.dumps({"status": "awaiting_input"}),
                        })
                    return self._pause(context, req["kind"], req["prompt"],
                                       req.get("options"))

            if not tool_calls:
                return self._ok_result(content or "")

            outputs = self._run_parallel_tools(tool_calls)
            for tc, out in outputs:
                self._report_tool_result(tc, out)
                context.append({
                    "role": C.ROLE_TOOL,
                    "tool_call_id": tc.get("id"),
                    "content": json.dumps(out, ensure_ascii=False),
                })
        raise ExecutionError(C.ERR_EXECUTION_TIMEOUT, "ExecutionTimeout")

    def _report_tool_calls(self, tool_calls: List[Dict[str, Any]]) -> None:
        client = getattr(self.model, "event_client", None)
        if client is None:
            return
        for tc in tool_calls:
            fn = tc.get("function") or {}
            try:
                client.emit_agent_tool_call(str(tc.get("id") or ""),
                                            str(fn.get("name") or "")[:128])
            except Exception:
                pass

    def _report_tool_result(self, tc: Dict[str, Any], out: Any) -> None:
        client = getattr(self.model, "event_client", None)
        if client is None:
            return
        fn = tc.get("function") or {}
        failed = not isinstance(out, dict) or out.get("ok") is False
        try:
            client.emit_agent_tool_result(
                str(tc.get("id") or ""), str(fn.get("name") or "")[:128],
                "failed" if failed else "succeeded",
                str(out.get("error_type") or "")[:128] if failed and isinstance(out, dict) else "")
        except Exception:
            pass

    def _run_parallel_tools(self, tool_calls: List[Dict[str, Any]]):
        out_by_id: Dict[str, Any] = {}
        with ThreadPoolExecutor(max_workers=min(len(tool_calls), 8)) as pool:
            futures = {}
            for tc in tool_calls:
                fn = (tc.get("function") or {}).get("name", "")
                args = (tc.get("function") or {}).get("arguments", "{}")
                futures[pool.submit(self._invoke_tool, fn, args)] = tc.get("id")
            for fut in as_completed(futures):
                out_by_id[futures[fut]] = fut.result()
        return [(tc, out_by_id.get(tc.get("id"))) for tc in tool_calls]

    def _invoke_tool(self, name: str, arguments: str) -> Dict[str, Any]:
        self._cancel_check()
        handler = self.tools.get(name)
        if handler is None:
            return {"ok": False, "error_type": "ToolNotFound",
                    "error": f"no such tool {name!r}"}
        try:
            return handler(name, arguments)
        except Exception as exc:  # noqa: BLE001 - surfaced as a tool result
            detail = str(exc).strip() or "tool execution failed"
            return {"ok": False, "error_type": type(exc).__name__,
                    "error": detail[:500]}


def run_entry(config: Dict[str, Any],
              model: Optional[ModelBackend] = None,
              steering: Optional[SafeNodeCoordinator] = None,
              tools: Optional[Dict[str, ToolRunner]] = None,
              request_input_policy: Optional[Callable[[List[Dict[str, Any]]], Optional[Dict[str, Any]]]] = None,
              cancel_event: Optional[threading.Event] = None,
              workspace_dir: str = C.WORKSPACE_DIR,
              output_dir: str = C.OUTPUT_DIR,
              checkpoint_path: str = C.CHECKPOINT_PATH,
              result_path: str = C.RESULT_PATH,
              resume_hook: Optional[Callable[[], None]] = None,
              unavailable_tools: Optional[List[Dict[str, str]]] = None) -> Dict[str, Any]:
    from .model import ModelGateway
    from .steering import SteeringClient
    from . import constants

    unavailable = list(unavailable_tools or [])

    if model is None:
        model_cfg = config.get("model") or {}
        rt = config.get("runtime") or {}
        event_client = None
        try:
            from .events import EventClient
            event_client = EventClient(
                rt.get("base_url", ""), rt.get("token", ""),
                config.get("execution_id", ""), run_id=config.get("run_id", ""))
        except Exception:  # noqa: BLE001 - fall back to non-reporting streaming
            event_client = None
        tool_specs: List[Dict[str, Any]] = []
        spec_unavailable: List[Dict[str, str]] = []
        try:
            from .tools import build_tool_specs_lenient
            from .tools.builtin import BUILTIN_TOOL_SPECS
            specs, spec_unavailable = build_tool_specs_lenient(config.get("tools") or [])
            tool_specs = specs + list(BUILTIN_TOOL_SPECS)
        except Exception:  # noqa: BLE001 - never fail the run on schema build
            pass
        unavailable = list(unavailable_tools or []) + spec_unavailable
        model = ModelGateway(model_cfg.get("base_url", ""),
                             model_cfg.get("token", ""),
                             model_cfg.get("name", ""),
                             event_client=event_client,
                             run_id=config.get("run_id", ""),
                             tools=tool_specs,
                             reasoning_effort=model_cfg.get("reasoning_effort"))
    if steering is None:
        rt = config.get("runtime") or {}
        steer = SteeringClient(rt.get("base_url", ""), rt.get("token", ""),
                               config.get("execution_id", ""))
        steering = SafeNodeCoordinator(
            steer, config.get("execution_id", ""),
            initial_cursor=int(((config.get("steering")) or {}).get("after_seq", 0)),
            remaining_seconds_provider=lambda: run_budget_guard(config),
            resource_injector=_steer_resource_injector(config, workspace_dir),
        )

    coord = RuntimeCoordinator(config, model, steering=steering, tools=tools,
                               request_input_policy=request_input_policy,
                               cancel_event=cancel_event,
                               workspace_dir=workspace_dir,
                               output_dir=output_dir,
                               checkpoint_path=checkpoint_path,
                               resume_hook=resume_hook,
                               builtin_prompt=_builtin_prompt(config),
                               unavailable_tools=unavailable,
                               artifact_bundler=ArtifactBundler(
                                   workspace_dir, output_dir,
                                   result_bundle=config.get("result_bundle")),
                               attach_context=_collect_attach_context(config, workspace_dir))
    try:
        return coord.run()
    finally:
        closer = getattr(getattr(model, "event_client", None), "close", None)
        if callable(closer):
            closer()


def _unavailable_tools_notice(unavailable: List[Dict[str, str]]) -> str:
    lines = ["The following external tools are unavailable for this run "
             "(discovery/connection failed) and must not be called."]
    lines.append("If the task depends on them, use another approach or tell the "
                 "user honestly that the tool is currently unavailable and why:")
    for item in unavailable:
        lines.append(f"- tool '{item.get('tool_id', '')}': {item.get('error', '')}")
    return "\n".join(lines)


def run_budget_guard(config: Dict[str, Any]) -> float:
    return float((config.get("limits") or {}).get(
        "remaining_execution_seconds", C.DEFAULT_REMAINING_EXECUTION_SECONDS))




def _builtin_prompt(config: Dict[str, Any]) -> Optional[str]:
    try:
        from .prompt import build_builtin_system_prompt
        return build_builtin_system_prompt(config)
    except Exception:  # noqa: BLE001 - best effort, never fail the Run
        return None


def _collect_attach_context(config: Dict[str, Any],
                            workspace_dir: str) -> List[Dict[str, Any]]:
    from .resources import collect_injectable_text
    injected: List[Dict[str, Any]] = []
    try:
        pairs = collect_injectable_text(config.get("files") or [], workspace_dir)
        injected.extend(
            {"role": "system", "content": f"附件《{name}》内容如下：\n{text}"}
            for name, text in pairs
        )
    except Exception:  # noqa: BLE001 - best effort
        pass
    for skill in config.get("skills") or []:
        if not isinstance(skill, dict):
            continue
        name = skill.get("name")
        if not name:
            continue
        md_path = os.path.join(workspace_dir, ".skills", name, "SKILL.md")
        try:
            with open(md_path, encoding="utf-8", errors="replace") as fh:
                text = fh.read(1024 * 1024 + 1)
        except OSError:
            continue
        if len(text) > 1024 * 1024:
            text = text[:1024 * 1024]
        if text.strip():
            injected.append(
                {"role": "system", "content": f"技能《{name}》说明如下：\n{text}"})
    return injected


def _steer_resource_injector(
    config: Dict[str, Any], workspace_dir: str
) -> Callable[[Dict[str, Any]], List[Dict[str, Any]]]:
    from .resources import ResourceAdapter, collect_injectable_text

    def inject(message: Dict[str, Any]) -> List[Dict[str, Any]]:
        files = message.get("files") or []
        skills = message.get("skills") or []
        if not files and not skills:
            return []
        adapter = ResourceAdapter()
        injected: List[Dict[str, Any]] = []
        try:
            if files:
                adapter.prepare_files(files, workspace_dir)
                for name, text in collect_injectable_text(files, workspace_dir):
                    injected.append(
                        {"role": "system",
                         "content": f"附件《{name}》内容如下：\n{text}"})
            if skills:
                for meta in adapter.prepare_skills(skills, workspace_dir):
                    md_path = os.path.join(
                        workspace_dir, meta.get("skill_dir", ""), "SKILL.md")
                    try:
                        with open(md_path, encoding="utf-8",
                                  errors="replace") as fh:
                            text = fh.read(1024 * 1024 + 1)
                    except OSError:
                        continue
                    if len(text) > 1024 * 1024:
                        text = text[:1024 * 1024]
                    if text.strip():
                        injected.append(
                            {"role": "system",
                             "content": f"技能《{meta.get('name', '')}》说明如下：\n{text}"})
        except Exception:  # noqa: BLE001 - best effort, never fail the Run
            return []
        return injected

    return inject


__all__ = [
    "RuntimeCoordinator", "run_entry", "ExecutionError", "CancelRequested",
    "REFERENCE_STATE_FORMAT",
]
