"""Safe model-failure transport and actual SDK classification, no paid calls."""
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading
from uuid import uuid4

import pytest

from mgar_agents.core.agent import Agent
from mgar_agents.core.claim import Claim
from mgar_agents.core.errors import ProviderFailure
from mgar_agents.core.provider import ModelRequest
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task
from mgar_agents.core.tools import ToolRegistry
from mgar_agents.runtime_worker import RemoteMock


@contextmanager
def bridge(status, body):
    requests = []
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            requests.append(self.rfile.read(int(self.headers['Content-Length'])))
            self.send_response(status)
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        def log_message(self, *_):
            pass
    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield RemoteMock(f'http://127.0.0.1:{server.server_port}', 'local-fixture-only'), requests
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def envelope(kind, usage):
    return {'schema_version': 'model-error.v1', 'error': kind, 'usage_status': usage, 'retryable': False}


@pytest.mark.parametrize('status,kind,usage', [
    (422, 'model_output_limit', 'known'), (422, 'model_invalid_response', 'known'),
    (413, 'model_input_limit', 'not_sent'), (402, 'budget_exhausted', 'not_sent'),
    (423, 'resource_exhausted', 'not_sent'), (403, 'policy_denied', 'not_sent'),
    (409, 'model_usage_unknown', 'unknown'),
])
def test_model_failure_reaches_sdk_without_policy_alias_or_retry(status, kind, usage):
    with bridge(status, json.dumps(envelope(kind, usage)).encode()) as (provider, requests):
        claim = Claim(str(uuid4()), str(uuid4()), 'worker', 'research', 1, 0, 1)
        snapshot = ContextSnapshot.from_dict({'schema_version': 'context.v1', 'task_id': claim.task_id,
            'plan_version': 1, 'model_config_version': 'mock.v1', 'tool_policy_version': 'fixture.v1', 'artifacts': []})
        result = Agent(provider).run(Task(claim, 'bounded fixture', 'development-fixture'), snapshot, ToolRegistry([]), Budget())
        assert result.status == 'failed' and result.error_kind == kind
        assert result.turns == 1 and len(requests) == 1


@pytest.mark.parametrize('status,payload', [
    (503, b'Bearer synthetic-leak-marker'), (422, b'{broken'),
    (403, json.dumps(envelope('model_output_limit', 'known')).encode()),
    (422, json.dumps(envelope('model_output_limit', 'unknown')).encode()),
    (422, json.dumps(envelope('model_output_limit', 'known') | {'retryable': True}).encode()),
    (422, json.dumps(envelope('model_output_limit', 'known') | {'details': 'synthetic-leak-marker'}).encode()),
    (422, b'x' * 65537),
])
def test_invalid_or_mismatched_envelopes_stay_unknown_and_redacted(status, payload):
    request = ModelRequest('run', 'task', 1, 1, 'call', 'development-fixture', 'goal', '{}', (), '[]')
    with bridge(status, payload) as (provider, requests):
        with pytest.raises(ProviderFailure) as caught:
            provider.decide(request, timeout_s=3)
        assert caught.value.kind == 'model_usage_unknown' and caught.value.status == 'unknown'
        assert 'synthetic-leak-marker' not in str(caught.value)
        assert len(requests) == 1
