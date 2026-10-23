"""P3 context identity additions remain compatible with the SDK wire protocol."""
from uuid import uuid4

import pytest

from mgar_agents.core.agent import Agent
from mgar_agents.core.claim import Claim
from mgar_agents.core.provider import MockProvider
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task
from mgar_agents.core.tools import ToolRegistry


@pytest.mark.parametrize('field,value', [('run_id', '00000000-0000-4000-8000-000000000001'), ('role', 'action')])
def test_context_wire_identity_cannot_override_task(field, value):
    claim = Claim(str(uuid4()), str(uuid4()), 'fixture-worker', 'research', 1, 0, 1)
    task = Task(claim, 'inspect fixed context')
    raw = {'schema_version': 'context.v1', 'snapshot_id': str(uuid4()), 'run_id': claim.run_id,
        'task_id': claim.task_id, 'role': claim.role, 'plan_version': 1,
        'model_config_version': 'mock.v1', 'tool_policy_version': 'research.v1', 'artifacts': []}
    raw[field] = value
    provider = MockProvider(['{"type":"final","output":{}}'])
    result = Agent(provider).run(task, ContextSnapshot.from_dict(raw), ToolRegistry([]), Budget())
    assert result.error_kind == 'policy_denied' and provider.requests == []
