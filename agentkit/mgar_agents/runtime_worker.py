"""One bounded SDK invocation. Authority and model-call accounting remain in Go."""
from __future__ import annotations

from dataclasses import asdict
import http.client
import json
import os
import sys
from typing import Any
from urllib.parse import urlsplit

from mgar_agents.core.agent import Agent
from mgar_agents.core.claim import Claim
from mgar_agents.core.gateway import GatewayClient
from mgar_agents.core.provider import ModelRequest
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task, canonical_json, strict_json
from mgar_agents.core.tools import ToolSpec


class RemoteMock:
    def __init__(self, endpoint: str, token: str) -> None:
        self.endpoint, self.token = urlsplit(endpoint), token

    def decide(self, request: ModelRequest, *, timeout_s: float) -> str:
        host = self.endpoint.hostname
        if host is None:
            raise ValueError('missing trusted endpoint')
        connection = http.client.HTTPConnection(host, self.endpoint.port, timeout=min(timeout_s, 30))
        try:
            payload = asdict(request)
            payload['observations'] = list(payload['observations'])
            connection.request('POST', '/v1/models', canonical_json(payload).encode(),
                               {'Authorization': 'Bearer ' + self.token, 'Content-Type': 'application/json'})
            response = connection.getresponse()
            raw = response.read(65537)
            if response.status != 200 or len(raw) > 65536:
                raise PermissionError('model admission rejected')
            return raw.decode()
        finally:
            connection.close()


def main() -> None:
    raw = sys.stdin.buffer.read(65537)
    if len(raw) > 65536:
        raise ValueError('job limit')
    job = strict_json(raw.decode())
    claim = Claim(**{key: job['claim'][key] for key in ('run_id', 'task_id', 'owner', 'role', 'epoch', 'revision', 'plan_version')})
    client = GatewayClient(job['endpoint'], job['token'])
    declarations = []
    for d in job['tools']:
        declarations.append(ToolSpec(name=d['name'], arguments_schema=d['schema'], handler=lambda **_: None,
                                     roles=frozenset({claim.role}), scope='fixture', effect_class=d['effect'],
                                     schema_version='demo.v1', timeout_s=30, max_output_bytes=60000))
    result = Agent(RemoteMock(job['endpoint'], job['token'])).run(
        Task(claim, job['goal'], job['case_id']), ContextSnapshot(job['snapshot']),
        client.registry(declarations), Budget(max_turns=8, max_tool_calls=6, timeout_s=90, max_observation_bytes=60000))
    out: dict[str, Any] = result.to_dict()
    out['pid'] = os.getpid()
    print(json.dumps(out, ensure_ascii=False, allow_nan=False))


if __name__ == '__main__':
    main()
