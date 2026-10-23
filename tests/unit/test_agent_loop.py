"""P2-01 real trusted-tool loop and refusal assertions, entirely offline."""
import json
import signal
import time
from pathlib import Path
from uuid import uuid4

import pytest

from mgar_agents.core.agent import Agent
from mgar_agents.core.claim import Claim
from mgar_agents.core.provider import MockProvider
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task
from mgar_agents.core.tools import ToolRegistry, ToolSpec


def task_and_snapshot(role="research"):
    claim = Claim(str(uuid4()), str(uuid4()), "fixture-worker", role, 1, 0, 1)
    task = Task(claim, "Investigate only synthetic fixture", "agent-loop-offline")
    snapshot = ContextSnapshot.from_dict({
        "schema_version": "context.v1", "task_id": claim.task_id, "plan_version": 1,
        "model_config_version": "mock.v1", "tool_policy_version": "local-fixture.v1", "artifacts": [],
    })
    return task, snapshot


def decision(name="read_fixture", arguments=None, call_id="call-1"):
    return json.dumps({"type": "tool_call", "call_id": call_id, "name": name, "arguments": arguments or {}})


def final(output=None):
    return json.dumps({"type": "final", "output": output or {"statement": "unverified fixture result"}})


def registry(handler, **kwargs):
    return ToolRegistry([ToolSpec(name="read_fixture", arguments_schema={"type": "object", "properties": {}, "additionalProperties": False}, handler=handler, roles=frozenset({"research"}), scope="fixture", **kwargs)])


def test_two_turns_observe_real_tool_before_final(tmp_path):
    source = tmp_path / "clue.txt"
    source.write_text("deployment introduced duplicate rows", encoding="utf-8")
    calls = []
    def read(arguments, *, timeout_s):
        calls.append((arguments, timeout_s))
        return {"text": source.read_text(encoding="utf-8")}
    def choose(request):
        if request.turn == 1:
            assert request.observations == ()
            return decision()
        assert request.observations[0].output["text"] == source.read_text()
        return final({"statement": request.observations[0].output["text"]})
    task, snapshot = task_and_snapshot()
    result = Agent(MockProvider(choose)).run(task, snapshot, registry(read), Budget())
    assert result.status == "completed" and result.turns == 2 and result.tool_calls == 1
    assert result.verified is False and result.output["statement"] == source.read_text()
    assert len(calls) == 1 and result.error_kind is None
    wire = result.to_dict()
    assert wire["run_id"] == task.claim.run_id and wire["trace_id"] == task.claim.run_id
    assert wire["task_id"] == task.claim.task_id and wire["epoch"] == 1
    assert [e["kind"] for e in wire["events"]] == ["run_started", "model_requested", "model_decided", "tool_requested", "tool_observed", "model_requested", "model_decided", "run_completed"]
    assert source.read_text() not in json.dumps(wire["events"])


@pytest.mark.parametrize("response,expected", [
    ("{broken", "invalid_response"),
    ('{"type":"final","type":"final","output":{}}', "invalid_response"),
    ('{"type":"final","output":{"x":NaN}}', "invalid_response"),
    ('{"Type":"final","output":{}}', "invalid_response"),
    (decision("shell"), "policy_denied"),
    (decision(arguments={"execute": "rm fake"}), "policy_denied"),
    ('{"type":"final","output":{},"verified":true}', "invalid_response"),
])
def test_invalid_decisions_never_execute(response, expected):
    calls = []
    def tool(arguments, *, timeout_s):
        calls.append(arguments)
        return {}
    task, snapshot = task_and_snapshot()
    result = Agent(MockProvider([response])).run(task, snapshot, registry(tool), Budget())
    assert result.status == "failed" and result.error_kind == expected and result.error_turn == 1
    assert calls == [] and result.verified is False


def test_repeat_call_stops_before_second_execution():
    calls = []
    def tool(arguments, *, timeout_s):
        calls.append(arguments)
        return {"value": 7}
    task, snapshot = task_and_snapshot()
    result = Agent(MockProvider([decision(), decision(call_id="different-id")])).run(task, snapshot, registry(tool), Budget())
    assert result.error_kind == "repeated_tool_call" and result.error_turn == 2 and len(calls) == 1


def test_role_and_effect_denials_are_terminal():
    def forbidden(arguments, *, timeout_s):
        pytest.fail("denied handler executed")
    task, snapshot = task_and_snapshot("data")
    result = Agent(MockProvider([decision()])).run(task, snapshot, registry(forbidden), Budget())
    assert result.error_kind == "policy_denied" and result.tool_calls == 0
    task, snapshot = task_and_snapshot()
    result = Agent(MockProvider([decision()])).run(task, snapshot, registry(forbidden, effect_class="non_idempotent"), Budget())
    assert result.error_kind == "policy_denied"


def test_turn_and_tool_budgets_are_not_model_controlled():
    def tool(arguments, *, timeout_s):
        return {"value": 1}
    task, snapshot = task_and_snapshot()
    result = Agent(MockProvider([decision(), final()])).run(task, snapshot, registry(tool), Budget(max_turns=1))
    assert result.error_kind == "budget_exhausted" and result.turns == 1
    result = Agent(MockProvider([decision()])).run(task, snapshot, registry(tool), Budget(max_tool_calls=0))
    assert result.error_kind == "budget_exhausted" and result.tool_calls == 0


def test_timeout_interrupts_tool_and_restores_signal_handler():
    def slow(arguments, *, timeout_s):
        time.sleep(2)
        raise AssertionError("deadline failed")
    task, snapshot = task_and_snapshot()
    before = signal.getsignal(signal.SIGALRM)
    started = time.monotonic()
    result = Agent(MockProvider([decision()])).run(task, snapshot, registry(slow, timeout_s=.03), Budget(timeout_s=.2))
    assert result.error_kind == "timeout" and result.error_tool == "read_fixture"
    assert time.monotonic() - started < .5
    assert signal.getsignal(signal.SIGALRM) == before and signal.getitimer(signal.ITIMER_REAL) == (0, 0)


def test_provider_deadline_is_enforced_not_just_passed_as_argument():
    def slow(request):
        time.sleep(2)
        return final()
    task, snapshot = task_and_snapshot()
    started = time.monotonic()
    result = Agent(MockProvider(slow)).run(task, snapshot, ToolRegistry([]), Budget(timeout_s=.03))
    assert result.error_kind == "timeout" and time.monotonic() - started < .5


def test_tool_permission_exception_stops_without_leaking_secret():
    def denied(arguments, *, timeout_s):
        raise PermissionError("private password must not appear")
    task, snapshot = task_and_snapshot()
    provider = MockProvider([decision(), final()])
    result = Agent(provider).run(task, snapshot, registry(denied), Budget())
    assert result.error_kind == "policy_denied" and len(provider.requests) == 1
    assert "private password" not in json.dumps(result.to_dict())


def test_result_schema_and_snapshot_identity_guards():
    from jsonschema import Draft202012Validator, FormatChecker
    task, snapshot = task_and_snapshot()
    result = Agent(MockProvider([final()])).run(task, snapshot, ToolRegistry([]), Budget())
    schema = json.loads((Path(__file__).parents[2] / "contracts/agent_result.schema.json").read_text())
    Draft202012Validator(schema, format_checker=FormatChecker()).validate(result.to_dict())
    raw = snapshot.to_dict()
    raw["task_id"] = str(uuid4())
    mismatch = Agent(MockProvider([final()])).run(task, ContextSnapshot.from_dict(raw), ToolRegistry([]), Budget())
    assert mismatch.error_kind == "policy_denied" and mismatch.turns == 0


@pytest.mark.parametrize("identity", ["123456781234123412341234567890ab", "{12345678-1234-1234-1234-1234567890ab}"])
def test_task_uuid_matches_result_schema_spelling(identity):
    claim = Claim(identity, str(uuid4()), "fixture-worker", "research", 1, 0, 1)
    with pytest.raises(ValueError):
        Task(claim, "synthetic fixture")
