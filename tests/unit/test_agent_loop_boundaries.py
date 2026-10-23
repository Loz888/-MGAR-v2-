"""Independent malformed-input and aliasing regression checks for the SDK."""
import json
import signal
from uuid import uuid4

import pytest

from mgar_agents.core.agent import Agent
from mgar_agents.core.claim import Claim
from mgar_agents.core.provider import MockProvider
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task
from mgar_agents.core.tools import ToolRegistry, ToolSpec


def inputs():
    claim = Claim(str(uuid4()), str(uuid4()), "boundary-worker", "research", 1, 0, 1)
    task = Task(claim, "Inspect the synthetic input", "sdk-boundaries")
    raw = {"schema_version": "context.v1", "task_id": claim.task_id, "plan_version": 1,
           "model_config_version": "mock.v1", "tool_policy_version": "trusted-fixture.v1", "artifacts": []}
    return task, ContextSnapshot.from_dict(raw)


def call(arguments):
    return json.dumps({"type": "tool_call", "call_id": "boundary-call", "name": "read", "arguments": arguments})


def done(output):
    return json.dumps({"type": "final", "output": output})


def tools(schema, handler):
    return ToolRegistry([ToolSpec(name="read", arguments_schema=schema, handler=handler,
                                 roles=frozenset({"research"}), scope="fixture")])


@pytest.mark.parametrize("envelope", ["final", "tool_call"])
def test_deep_json_returns_locatable_failure_without_executing(envelope):
    task, snapshot = inputs()
    deep = "[" * 1500 + "0" + "]" * 1500
    raw = ('{"type":"final","output":{"nested":' + deep + '}}' if envelope == "final" else
           '{"type":"tool_call","call_id":"a","name":"read","arguments":{"nested":' + deep + '}}')
    executed = []

    def handler(arguments, *, timeout_s):
        executed.append(arguments)
        return {}

    result = Agent(MockProvider([raw])).run(task, snapshot, tools({"type": "object", "properties": {}, "additionalProperties": False}, handler), Budget())
    assert result.status == "failed" and result.error_kind == "invalid_response"
    assert result.error_turn == 1 and result.tool_calls == 0 and executed == []


@pytest.mark.parametrize("name,value", [("max_turns", True), ("max_turns", 1.0),
                                       ("max_tool_calls", False), ("max_observation_bytes", True),
                                       ("timeout_s", float("nan")), ("timeout_s", float("inf")),
                                       ("timeout_s", True)])
def test_budget_rejects_boolean_counters_and_nonfinite_deadlines(name, value):
    with pytest.raises((ValueError, TypeError)):
        Budget(**{name: value})


@pytest.mark.parametrize("field,value", [("revision", True), ("plan_version", True), ("revision", 1.0)])
def test_task_rejects_noninteger_claim_versions(field, value):
    values = {"task_id": str(uuid4()), "run_id": str(uuid4()), "owner": "worker", "role": "research",
              "epoch": 1, "revision": 0, "plan_version": 1}
    values[field] = value
    with pytest.raises((ValueError, TypeError)):
        Task(Claim(**values), "synthetic goal", "sdk-boundaries")


def test_tool_argument_format_is_checked_before_execution():
    task, snapshot = inputs()
    executed = []

    def handler(arguments, *, timeout_s):
        executed.append(arguments)
        return {}

    schema = {"type": "object", "properties": {"artifact_id": {"type": "string", "format": "uuid"}},
              "required": ["artifact_id"], "additionalProperties": False}
    result = Agent(MockProvider([call({"artifact_id": "not-a-uuid"})])).run(task, snapshot, tools(schema, handler), Budget())
    assert result.error_kind == "policy_denied" and executed == []


def test_mutating_original_schema_cannot_expand_authorized_arguments():
    task, snapshot = inputs()
    executed = []

    def handler(arguments, *, timeout_s):
        executed.append(arguments)
        return {}

    permitted = ["fixture.txt"]
    schema = {"type": "object", "properties": {"path": {"enum": permitted}},
              "required": ["path"], "additionalProperties": False}
    registry = tools(schema, handler)
    permitted.append("unauthorized.txt")
    schema["additionalProperties"] = True
    result = Agent(MockProvider([call({"path": "unauthorized.txt"})])).run(task, snapshot, registry, Budget())
    assert result.error_kind == "policy_denied" and executed == []


def test_tool_return_alias_and_observation_copies_cannot_rewrite_history():
    task, snapshot = inputs()
    original = {"nested": {"value": "recorded"}}

    def handler(arguments, *, timeout_s):
        return original

    def choose(request):
        if request.turn == 1:
            return call({})
        original["nested"]["value"] = "rewritten"
        view = request.observations[0].output
        assert view["nested"]["value"] == "recorded"
        view["nested"]["value"] = "client-mutated-copy"
        assert request.observations[0].output["nested"]["value"] == "recorded"
        return done({"value": request.observations[0].output["nested"]["value"]})

    result = Agent(MockProvider(choose)).run(task, snapshot, tools({"type": "object", "properties": {}, "additionalProperties": False}, handler), Budget())
    assert result.status == "completed" and result.output == {"value": "recorded"}
    returned = result.to_dict()
    returned["output"]["value"] = "changed"
    assert result.output == {"value": "recorded"}


def test_existing_alarm_is_preserved_without_starting_provider():
    task, snapshot = inputs()
    provider = MockProvider([done({})])
    old_handler = signal.getsignal(signal.SIGALRM)
    old_timer = signal.getitimer(signal.ITIMER_REAL)
    fired = []
    signal.signal(signal.SIGALRM, lambda *_: fired.append(True))
    signal.setitimer(signal.ITIMER_REAL, 2)
    try:
        result = Agent(provider).run(task, snapshot, ToolRegistry([]), Budget(timeout_s=.1))
        assert result.status == "failed"
        assert provider.requests == []
        assert signal.getitimer(signal.ITIMER_REAL)[0] > 0
        assert fired == []
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, old_handler)
        signal.setitimer(signal.ITIMER_REAL, *old_timer)


@pytest.mark.parametrize("keyword,reference", [("$ref", "https://schemas.example.invalid/args.json"),
                                               ("$ref", "file:///etc/passwd"),
                                               ("$dynamicRef", "http://schemas.example.invalid/args.json"),
                                               ("$ref", "../outside-schema.json")])
def test_tool_declaration_rejects_external_schema_references(keyword, reference):
    def handler(arguments, *, timeout_s):
        pytest.fail("external schema declaration must not execute a tool")

    schema = {"type": "object", "properties": {"value": {keyword: reference}}, "additionalProperties": False}
    with pytest.raises((ValueError, PermissionError)):
        tools(schema, handler)


def test_local_schema_fragment_reference_is_validated_without_external_resources():
    task, snapshot = inputs()
    executed = []

    def handler(arguments, *, timeout_s):
        executed.append(arguments["value"])
        return {"echo": arguments["value"]}

    schema = {"type": "object", "properties": {"value": {"$ref": "#/$defs/token"}},
              "$defs": {"token": {"type": "string", "enum": ["allowed"]}},
              "required": ["value"], "additionalProperties": False}
    registry = tools(schema, handler)
    result = Agent(MockProvider([call({"value": "allowed"}), done({"status": "unverified"})])).run(task, snapshot, registry, Budget())
    assert result.status == "completed" and executed == ["allowed"]
    result = Agent(MockProvider([call({"value": "denied"})])).run(task, snapshot, registry, Budget())
    assert result.error_kind == "policy_denied" and executed == ["allowed"]


def _expensive_validation_process(connection):
    # A separate main-thread process lets this test terminate the broken regex
    # path instead of leaving pytest itself stuck when the SDK deadline fails.
    task, snapshot = inputs()
    executed = []

    def handler(arguments, *, timeout_s):
        executed.append(arguments)
        return {}

    schema = {"type": "object", "properties": {"value": {"type": "string", "pattern": "^(a+)+$"}},
              "required": ["value"], "additionalProperties": False}
    result = Agent(MockProvider([call({"value": "a" * 40 + "!"})])).run(task, snapshot, tools(schema, handler), Budget(timeout_s=.03))
    connection.send((result.error_kind, result.tool_calls, len(executed)))
    connection.close()


def test_total_deadline_interrupts_schema_validation():
    import multiprocessing

    context = multiprocessing.get_context("fork")
    read, write = context.Pipe(duplex=False)
    process = context.Process(target=_expensive_validation_process, args=(write,))
    process.start()
    write.close()
    try:
        returned = read.poll(2)
        if not returned:
            process.kill()
            process.join(timeout=2)
        assert returned, "schema validation escaped the SDK deadline; test child was killed"
        assert read.recv() == ("timeout", 0, 0)
        process.join(timeout=2)
        assert process.exitcode == 0
    finally:
        if process.is_alive():
            process.kill()
            process.join(timeout=2)
        read.close()
