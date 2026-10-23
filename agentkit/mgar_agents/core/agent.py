"""Bounded model→tool→observation loop; no direct control-plane database writes."""
from __future__ import annotations

import hashlib
from typing import Any
import time
from uuid import uuid4

from jsonschema.exceptions import ValidationError

from .provider import ModelRequest, Provider
from .schemas import AgentResult, Budget, ContextSnapshot, Event, Observation, Task, canonical_json, strict_json, validate
from .tools import ToolExecutionError, ToolRegistry, run_bounded

DECISION_SCHEMA: dict[str, Any] = {
    "oneOf": [
        {"type": "object", "additionalProperties": False, "required": ["type", "call_id", "name", "arguments"],
         "properties": {"type": {"const": "tool_call"}, "call_id": {"type": "string", "pattern": "^[A-Za-z0-9_-]{1,64}$"},
                        "name": {"type": "string", "pattern": "^[a-z][a-z0-9_.-]{0,63}$"}, "arguments": {"type": "object"}}},
        {"type": "object", "additionalProperties": False, "required": ["type", "output"],
         "properties": {"type": {"const": "final"}, "output": {"type": "object"}}},
    ]
}


class Agent:
    def __init__(self, provider: Provider) -> None:
        self.provider = provider

    def run(self, task: Task, snapshot: ContextSnapshot, tools: ToolRegistry, budget: Budget) -> AgentResult:
        events: list[Event] = []
        observations: list[Observation] = []
        turns = calls = 0
        tool_name: str | None = None
        def emit(kind: str, *, call_id: str | None = None, observation_hash: str | None = None, error_kind: str | None = None) -> None:
            events.append(Event(len(events) + 1, kind, turns, tool_name, call_id, observation_hash, error_kind))
        def failed(kind: str) -> AgentResult:
            emit("run_failed", error_kind=kind)
            return AgentResult(task, "failed", turns, calls, None, kind, turns, tool_name, tuple(observations), tuple(events))
        emit("run_started")
        snap = snapshot.to_dict()
        if snap["task_id"] != task.claim.task_id or snap["plan_version"] != task.claim.plan_version:
            return failed("policy_denied")
        seen_calls: set[str] = set()
        seen_requests: set[str] = set()
        deadline = time.monotonic() + budget.timeout_s
        try:
            for turn in range(1, budget.max_turns + 1):
                tool_name = None
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return failed("timeout")
                turns = turn
                emit("model_requested")
                request = ModelRequest(task.claim.run_id, task.claim.task_id, task.claim.plan_version, turn, str(uuid4()), task.case_id,
                                       task.goal, canonical_json(snap), tuple(observations), tools.declarations_json(task.claim.role))
                response = run_bounded(lambda: self.provider.decide(request, timeout_s=remaining), remaining)
                try:
                    decision = strict_json(response)
                    validate(DECISION_SCHEMA, decision)
                except (ValueError, ValidationError, RecursionError):
                    return failed("invalid_response")
                emit("model_decided")
                if time.monotonic() >= deadline:
                    return failed("timeout")
                if decision["type"] == "final":
                    emit("run_completed")
                    return AgentResult(task, "completed", turns, calls, canonical_json(decision["output"]), None, None, None, tuple(observations), tuple(events))
                tool_name = decision["name"]
                arguments = decision["arguments"]
                run_bounded(lambda: tools.authorize(tool_name, arguments, task.claim.role), deadline - time.monotonic())
                if calls >= budget.max_tool_calls:
                    return failed("budget_exhausted")
                call_id = decision["call_id"]
                digest = hashlib.sha256((tool_name + "\0" + canonical_json(arguments)).encode()).hexdigest()
                if call_id in seen_calls or digest in seen_requests:
                    return failed("repeated_tool_call")
                seen_calls.add(call_id)
                seen_requests.add(digest)
                calls += 1
                emit("tool_requested", call_id=call_id)
                output = tools.execute(tool_name, arguments, task.claim.role, timeout_s=deadline - time.monotonic(), max_output_bytes=budget.max_observation_bytes)
                output_hash = hashlib.sha256(output.encode()).hexdigest()
                observations.append(Observation(turns, call_id, tool_name, "ok", output, output_hash))
                emit("tool_observed", call_id=call_id, observation_hash=output_hash)
            return failed("budget_exhausted")
        except TimeoutError:
            return failed("timeout")
        except PermissionError:
            return failed("policy_denied")
        except ToolExecutionError:
            return failed("tool_error")
        except Exception:
            # Provider error details may contain credentials or raw prompts.
            return failed("provider_error")
