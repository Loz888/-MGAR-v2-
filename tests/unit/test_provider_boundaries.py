"""Independent Provider admission and cancellation boundary regressions."""
from contextlib import contextmanager
from dataclasses import replace
from decimal import Decimal
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import signal
import threading
import time
from uuid import uuid4

import pytest

from mgar_agents.core.provider import ModelRequest
from mgar_agents.core.tools import run_bounded


def model_request(case_id="boundary"):
    return ModelRequest(str(uuid4()), str(uuid4()), 1, 1, str(uuid4()), case_id,
                        "Inspect synthetic fixture", "{}", (), "[]")


def success_body():
    decision = json.dumps({"type": "final", "output": {"summary": "synthetic"}})
    return json.dumps({"id": "boundary-response", "object": "chat.completion", "model": "deepseek-chat",
                       "choices": [{"index": 0, "message": {"role": "assistant", "content": decision}, "finish_reason": "stop"}],
                       "usage": {"prompt_tokens": 7, "completion_tokens": 3}}).encode()


@contextmanager
def http_stub(*, drip=False, wait=False, header_drip=False, body_override=None):
    """Only a real local HTTP server; no external destination is contacted."""
    class CallLog(list):
        def __init__(self):
            super().__init__()
            self.received = threading.Event()

        def append(self, value):
            super().append(value)
            self.received.set()

    calls = CallLog()
    stop = threading.Event()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            self.rfile.read(int(self.headers["Content-Length"]))
            calls.append(self.path)
            body = success_body() if body_override is None else body_override
            if wait:
                stop.wait(.3)
            try:
                if header_drip:
                    header = b"HTTP/1.0 200 OK\r\nContent-Length: " + str(len(body)).encode() + b"\r\n\r\n"
                    for byte in header:
                        if stop.wait(.015):
                            return
                        self.connection.sendall(bytes([byte]))
                    self.connection.sendall(body)
                    return
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                if drip:
                    for byte in body:
                        if stop.wait(.015):
                            return
                        self.wfile.write(bytes([byte]))
                        self.wfile.flush()
                else:
                    self.wfile.write(body)
            except (BrokenPipeError, ConnectionResetError):
                pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=.005), daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_address[1]}/chat/completions", calls
    finally:
        stop.set()
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
        assert not thread.is_alive()


class Ledger:
    durable = False

    def __init__(self, mutation=None):
        self.reservations = []
        self.settlements = []
        self.mutation = mutation

    def reserve(self, request, call_id, *, estimate_bound, max_tokens, max_cost_usd, metadata):
        from mgar_agents.provider import Reservation
        reservation = Reservation(call_id, request.logical_call_id, request.run_id, request.task_id,
                                  request.plan_version, estimate_bound, max_tokens, committed=True,
                                  model=metadata["model"], model_config_version=metadata["model_config_version"],
                                  price_version=metadata["price_version"])
        if self.mutation:
            reservation = replace(reservation, **self.mutation)
        self.reservations.append(reservation)
        return reservation

    def settle(self, reservation, *, status, error_kind, usage, retain_bound):
        self.settlements.append({"status": status, "error_kind": error_kind,
                                 "usage": usage, "retain_bound": retain_bound})


def config(endpoint, **overrides):
    from mgar_agents.provider import ProviderConfig
    values = {"endpoint": endpoint, "api_key": "boundary-stub-key", "allow_paid": True,
              "max_cost_usd": Decimal("1"), "live_cases": frozenset({"boundary"}),
              "allow_loopback_stub": True, "timeout_s": .2, "retry_backoff_s": 0}
    values.update(overrides)
    return ProviderConfig(**values)


@pytest.mark.parametrize("field,value", [("call_id", str(uuid4())), ("logical_call_id", str(uuid4())),
                                         ("task_id", str(uuid4())), ("run_id", str(uuid4())),
                                         ("plan_version", 2), ("committed", False),
                                         ("estimate_bound", Decimal(".001")), ("max_tokens", 1),
                                         ("plan_version", True), ("plan_version", 1.0), ("max_tokens", 128.0),
                                         ("model", "deepseek-reasoner"), ("model_config_version", "other.v1"),
                                         ("price_version", "other.v1")])
def test_mismatched_or_uncommitted_reservation_never_sends_http(field, value):
    from mgar_agents.core.errors import ProviderFailure
    from mgar_agents.provider import DeepSeekProvider
    with http_stub() as (endpoint, calls):
        ledger = Ledger({field: value})
        provider = DeepSeekProvider(config(endpoint), ledger)
        with pytest.raises(ProviderFailure):
            provider.decide(model_request(), timeout_s=.2)
        assert calls == [], "HTTP was sent without the exact committed admission for this attempt"


@pytest.mark.parametrize("phase", ["headers", "body"])
def test_slow_response_bytes_cannot_extend_absolute_provider_deadline(phase):
    from mgar_agents.core.errors import ProviderFailure
    from mgar_agents.provider import DeepSeekProvider
    with http_stub(drip=phase == "body", header_drip=phase == "headers") as (endpoint, calls):
        ledger = Ledger()
        provider = DeepSeekProvider(config(endpoint, timeout_s=.06), ledger)
        before = time.monotonic()
        with pytest.raises(ProviderFailure):
            provider.decide(model_request(), timeout_s=.06)
        elapsed = time.monotonic() - before
        assert elapsed < .6, f"slow drip defeated the absolute deadline: {elapsed:.3f}s"
        assert len(calls) == len(ledger.reservations) == 1
        assert ledger.settlements[-1]["status"] == "unknown"
        assert ledger.settlements[-1]["usage"] is None
        assert ledger.settlements[-1]["retain_bound"] is True
        assert signal.getitimer(signal.ITIMER_REAL) == (0.0, 0.0)


def test_outer_sdk_deadline_records_unknown_and_retains_reservation():
    from mgar_agents.core.errors import ProviderFailure
    from mgar_agents.provider import DeepSeekProvider
    with http_stub(wait=True) as (endpoint, calls):
        ledger = Ledger()
        provider = DeepSeekProvider(config(endpoint, timeout_s=2), ledger)
        with pytest.raises((ProviderFailure, TimeoutError)):
            run_bounded(lambda: provider.decide(model_request(), timeout_s=2), .03)
        assert len(calls) == len(ledger.reservations) == 1
        assert ledger.settlements[-1]["status"] == "unknown"
        assert ledger.settlements[-1]["usage"] is None
        assert ledger.settlements[-1]["retain_bound"] is True
        assert signal.getitimer(signal.ITIMER_REAL) == (0.0, 0.0)


def test_case_allowlist_uses_exact_identity_before_reserve_or_http():
    from mgar_agents.core.errors import ProviderFailure
    from mgar_agents.provider import DeepSeekProvider
    with http_stub() as (endpoint, calls):
        ledger = Ledger()
        provider = DeepSeekProvider(config(endpoint), ledger)
        with pytest.raises(ProviderFailure):
            provider.decide(model_request("boundary-extra"), timeout_s=.2)
        assert ledger.reservations == [] and calls == []


def test_credential_is_absent_from_configuration_repr():
    with http_stub() as (endpoint, _):
        configured = config(endpoint)
        assert "boundary-stub-key" not in repr(configured)


def test_expired_admission_receipt_never_starts_http():
    from mgar_agents.core.errors import ProviderFailure
    from mgar_agents.provider import DeepSeekProvider

    class DelayedLedger(Ledger):
        def reserve(self, *args, **kwargs):
            receipt = super().reserve(*args, **kwargs)
            # A committed external admission can finish while cancellation is
            # being reconciled. A late receipt must never start a new request.
            try:
                time.sleep(.06)
            except TimeoutError:
                pass
            return receipt

    with http_stub() as (endpoint, calls):
        ledger = DelayedLedger()
        provider = DeepSeekProvider(config(endpoint, timeout_s=.02), ledger)
        with pytest.raises(ProviderFailure):
            provider.decide(model_request(), timeout_s=.02)
        calls.received.wait(.1)
        assert calls == [], "a receipt returned after its deadline initiated HTTP"


def test_valid_usage_is_preserved_when_the_model_decision_is_invalid():
    from mgar_agents.core.errors import ProviderFailure
    from mgar_agents.provider import DeepSeekProvider, Usage

    body = json.loads(success_body())
    body["choices"][0]["message"]["content"] = '{"type":"final","output":null}'
    with http_stub(body_override=json.dumps(body).encode()) as (endpoint, calls):
        ledger = Ledger()
        provider = DeepSeekProvider(config(endpoint), ledger)
        with pytest.raises(ProviderFailure) as caught:
            provider.decide(model_request(), timeout_s=.2)
        assert caught.value.kind == "invalid_response" and len(calls) == 1
        assert ledger.settlements[-1]["status"] == "failed"
        assert ledger.settlements[-1]["usage"] == Usage(7, 3), "known Provider usage was discarded on response rejection"


def test_boundary_http_fixture_is_a_valid_success_response():
    from mgar_agents.provider import DeepSeekProvider, Usage
    with http_stub() as (endpoint, calls):
        ledger = Ledger()
        output = DeepSeekProvider(config(endpoint), ledger).decide(model_request(), timeout_s=.2)
        assert json.loads(output) == {"type": "final", "output": {"summary": "synthetic"}}
        assert len(calls) == 1
        assert ledger.settlements[-1] == {"status": "succeeded", "error_kind": None,
                                           "usage": Usage(7, 3), "retain_bound": False}
