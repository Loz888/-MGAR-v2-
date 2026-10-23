"""Real loopback HTTP exercises; no external model request is made."""
from contextlib import contextmanager
from decimal import Decimal
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import socket
import threading
import time
from uuid import UUID, uuid4

import pytest

from mgar_agents.core.agent import Agent
from mgar_agents.core.claim import Claim
from mgar_agents.core.errors import ProviderFailure
from mgar_agents.core.provider import ModelRequest
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task
from mgar_agents.core.tools import ToolRegistry, ToolSpec
from mgar_agents.provider import DeepSeekProvider, ProviderConfig, Reservation, Usage, StructuredMockProvider


def request(**changes):
    values = dict(run_id=str(uuid4()), task_id=str(uuid4()), plan_version=1, turn=1,
                  logical_call_id=str(uuid4()), case_id="provider-fixture", goal="inspect synthetic data",
                  snapshot_json='{}', observations=(), tools_json='[]')
    values.update(changes)
    return ModelRequest(**values)


def envelope(content=None, usage=None):
    return {"id": "stub-response", "object": "chat.completion", "model": "deepseek-chat",
            "choices": [{"index": 0, "message": {"role": "assistant", "content": content or '{"type":"final","output":{"answer":7}}'}, "finish_reason": "stop"}],
            **({"usage": usage} if usage is not None else {})}


class Ledger:
    durable = False

    def __init__(self, max_calls=20):
        self.max_calls = max_calls
        self.events = []
        self.reservations = []

    def reserve(self, req, call_id, *, estimate_bound, max_tokens, max_cost_usd, metadata):
        if len(self.reservations) >= self.max_calls:
            raise ProviderFailure("budget_exhausted", "not_sent")
        reservation = Reservation(call_id, req.logical_call_id, req.run_id, req.task_id,
                                  req.plan_version, estimate_bound, max_tokens)
        self.reservations.append(reservation)
        self.events.append({"phase": "reserved", "call_id": call_id})
        return reservation

    def settle(self, reservation, *, status, error_kind, usage, retain_bound):
        self.events.append({"phase": "settled", "call_id": reservation.call_id, "status": status,
                            "error_kind": error_kind, "usage": usage, "retain_bound": retain_bound})


@contextmanager
def stub(responses, ledger=None):
    calls = []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length)
            calls.append(json.loads(body))
            if ledger is not None:
                assert ledger.events[-1]["phase"] == "reserved", "network before committed admission"
            index = min(len(calls) - 1, len(responses) - 1)
            item = responses[index]
            if item == "disconnect":
                self.connection.shutdown(socket.SHUT_RDWR)
                self.connection.close()
                return
            if item == "timeout":
                time.sleep(.15)
                item = (200, {}, envelope())
            status, headers, data = item
            raw = data if isinstance(data, bytes) else json.dumps(data).encode()
            self.send_response(status)
            for key, value in headers.items():
                self.send_header(key, value)
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            try:
                self.wfile.write(raw)
            except (BrokenPipeError, ConnectionResetError):
                pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": .01}, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}/chat/completions", calls
    finally:
        server.shutdown()
        server.server_close()
        thread.join(1)


def config(endpoint, **changes):
    values = dict(endpoint=endpoint, api_key="stub-credential", allow_paid=True,
                  max_cost_usd=Decimal("1"), live_cases=frozenset({"provider-fixture"}),
                  allow_loopback_stub=True, timeout_s=.5)
    values.update(changes)
    return ProviderConfig(**values)


def test_structured_mock_keeps_toolcall_and_final_protocol():
    model = StructuredMockProvider(['{"type":"tool_call","call_id":"c1","name":"read","arguments":{}}',
                                    '{"type":"final","output":{"done":true}}'])
    assert json.loads(model.decide(request(), timeout_s=1))["type"] == "tool_call"
    assert json.loads(model.decide(request(), timeout_s=1))["type"] == "final"


@pytest.mark.parametrize("changes", [{"allow_paid": False}, {"api_key": ""}, {"max_cost_usd": None},
                                      {"max_cost_usd": Decimal("NaN")}, {"max_cost_usd": Decimal("0")},
                                      {"live_cases": frozenset()}, {"allow_loopback_stub": False}])
def test_unauthorized_gates_send_zero_requests(changes):
    ledger = Ledger()
    with stub([(200, {}, envelope())], ledger) as (endpoint, calls):
        with pytest.raises((ProviderFailure, ValueError, PermissionError)):
            DeepSeekProvider(config(endpoint, **changes), ledger).decide(request(), timeout_s=1)
        assert calls == [] and ledger.events == []


def test_missing_ledger_denies_even_with_paid_flags():
    with stub([(200, {}, envelope())]) as (endpoint, calls):
        with pytest.raises(ProviderFailure) as caught:
            DeepSeekProvider(config(endpoint), None).decide(request(), timeout_s=1)
        assert caught.value.kind == "policy_denied" and calls == []


@pytest.mark.parametrize("status", [429, 503])
def test_known_not_processed_retries_with_distinct_physical_ids(status):
    ledger = Ledger()
    req = request()
    with stub([(status, {"X-MGAR-Not-Processed": "true"}, {}), (200, {}, envelope(usage={"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12}))], ledger) as (endpoint, calls):
        result = DeepSeekProvider(config(endpoint), ledger).decide(req, timeout_s=1)
    assert json.loads(result)["output"]["answer"] == 7 and len(calls) == 2
    assert len({r.call_id for r in ledger.reservations}) == 2
    assert {r.logical_call_id for r in ledger.reservations} == {req.logical_call_id}
    for r in ledger.reservations:
        UUID(r.call_id)
    assert ledger.events[1]["status"] == "failed" and ledger.events[1]["retain_bound"] is False
    assert ledger.events[-1]["usage"] == Usage(8, 4)
    assert ledger.events[-1]["status"] == "succeeded"


@pytest.mark.parametrize("response", [(429, {}, {}), (503, {}, {}), "disconnect", "timeout"])
def test_ambiguous_sent_request_is_unknown_without_replay(response):
    ledger = Ledger()
    with stub([response], ledger) as (endpoint, calls):
        with pytest.raises(ProviderFailure) as caught:
            DeepSeekProvider(config(endpoint, timeout_s=.03), ledger).decide(request(), timeout_s=.1)
        assert caught.value.status == "unknown" and len(calls) == 1
    assert len(ledger.reservations) == 1 and ledger.events[-1]["retain_bound"] is True
    assert ledger.events[-1]["usage"] is None


def test_retries_are_bounded_by_attempt_count_and_ledger_budget():
    for cap, attempts in [(20, 2), (1, 4)]:
        ledger = Ledger(max_calls=cap)
        with stub([(429, {"X-MGAR-Not-Processed": "true"}, {})], ledger) as (endpoint, calls):
            with pytest.raises(ProviderFailure):
                DeepSeekProvider(config(endpoint, max_attempts=attempts), ledger).decide(request(), timeout_s=1)
            assert len(calls) == min(cap, attempts)


@pytest.mark.parametrize("body", [b'{"choices":', b'{"choices":[],"choices":[]}',
                                   json.dumps(envelope(content='{"type":"final","output":{"n":NaN}}')).encode(),
                                   json.dumps(envelope(content='{"type":"final","output":null}')).encode(),
                                   json.dumps(envelope(usage={"prompt_tokens": True, "completion_tokens": 2})).encode()])
def test_malformed_response_rejected_and_usage_not_invented(body):
    ledger = Ledger()
    with stub([(200, {}, body)], ledger) as (endpoint, calls):
        with pytest.raises(ProviderFailure) as caught:
            DeepSeekProvider(config(endpoint), ledger).decide(request(), timeout_s=1)
        assert caught.value.kind == "invalid_response" and len(calls) == 1
    assert ledger.events[-1]["usage"] is None and ledger.events[-1]["retain_bound"] is True


def test_missing_usage_remains_unknown_even_for_success():
    ledger = Ledger()
    with stub([(200, {}, envelope())], ledger) as (endpoint, calls):
        DeepSeekProvider(config(endpoint), ledger).decide(request(), timeout_s=1)
    assert len(calls) == 1 and ledger.events[-1]["status"] == "succeeded"
    assert ledger.events[-1]["usage"] is None and ledger.events[-1]["retain_bound"] is True


def test_response_limit_redirect_and_secret_redaction():
    for status, headers, body in [(200, {}, b'x' * 4000), (302, {"Location": "https://invalid.example/"}, b'credential-in-body')]:
        ledger = Ledger()
        with stub([(status, headers, body)], ledger) as (endpoint, calls):
            cfg = config(endpoint, max_response_bytes=1024)
            with pytest.raises(ProviderFailure) as caught:
                DeepSeekProvider(cfg, ledger).decide(request(), timeout_s=1)
            assert len(calls) == 1
            assert "stub-credential" not in str(caught.value) + repr(cfg) + repr(ledger.events)
            assert "credential-in-body" not in str(caught.value) + repr(ledger.events)


def test_native_toolcall_is_parsed_but_registry_denies_bad_arguments():
    ledger = Ledger()
    native = envelope()
    native['choices'][0]['message'] = {"role": "assistant", "content": None, "tool_calls": [{"id": "call_native", "type": "function", "function": {"name": "read", "arguments": '{"path":"outside"}'}}]}
    native['choices'][0]['finish_reason'] = 'tool_calls'
    executed = []
    def read(args, *, timeout_s):
        executed.append(args)
        return {}
    with stub([(200, {}, native)], ledger) as (endpoint, calls):
        req = request()
        task = Task(Claim(req.task_id, req.run_id, 'fixture-worker', 'research', 1, 0, 1), 'fixture', req.case_id)
        snapshot = ContextSnapshot.from_dict({'schema_version':'context.v1','task_id':req.task_id,'plan_version':1,'model_config_version':'mock.v1','tool_policy_version':'fixture.v1','artifacts':[]})
        tools = ToolRegistry([ToolSpec('read', {'type':'object','properties':{'path':{'enum':['allowed']}},'required':['path'],'additionalProperties':False},read,roles=frozenset({'research'}),scope='fixture')])
        result = Agent(DeepSeekProvider(config(endpoint), ledger)).run(task, snapshot, tools, Budget())
        assert result.error_kind == 'policy_denied' and result.error_turn == 1 and executed == [] and len(calls) == 1


def test_from_env_is_default_off_and_does_not_expose_credential():
    cfg = ProviderConfig.from_env({})
    assert cfg.allow_paid is False and cfg.max_cost_usd is None and cfg.live_cases == frozenset()
    assert 'secret-value' not in repr(ProviderConfig.from_env({'DEEPSEEK_API_KEY':'secret-value'}))


@pytest.mark.parametrize("change", [{"model": "wrong-model"}, {"index": False}, {"index": 0.0}])
def test_response_model_and_choice_index_follow_frozen_config(change):
    body = envelope()
    if 'model' in change:
        body['model'] = change['model']
    else:
        body['choices'][0]['index'] = change['index']
    ledger = Ledger()
    with stub([(200, {}, body)], ledger) as (endpoint, calls):
        with pytest.raises(ProviderFailure) as caught:
            DeepSeekProvider(config(endpoint), ledger).decide(request(), timeout_s=1)
        assert caught.value.kind == 'invalid_response' and len(calls) == 1
