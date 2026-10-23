"""Action proposals require actual current-workspace test evidence."""
from __future__ import annotations

from dataclasses import replace
import hashlib
from typing import Any
from uuid import NAMESPACE_URL, uuid5

from jsonschema.exceptions import ValidationError

from mgar_agents.core.agent import Agent
from mgar_agents.core.provider import Provider
from mgar_agents.core.schemas import AgentResult, Budget, ContextSnapshot, Event, Task, canonical_json, validate

from .workspace import ActionWorkspace

FINAL_SCHEMA: dict[str, Any] = {'type': 'object', 'additionalProperties': False,
    'required': ['summary', 'patch_id', 'test_call_id', 'workspace_sha256', 'passed'], 'properties': {
        'summary': {'type': 'string', 'minLength': 1, 'maxLength': 4096, 'pattern': r'\S'},
        'patch_id': {'enum': ['deduplicate-only', 'deduplicate-paid']},
        'test_call_id': {'type': 'string', 'minLength': 1, 'maxLength': 64},
        'workspace_sha256': {'type': 'string', 'pattern': '^[0-9a-f]{64}$'}, 'passed': {'const': True}}}


class ActionAgent:
    def __init__(self, provider: Provider, workspace: ActionWorkspace) -> None:
        self.provider = provider
        self.workspace = workspace

    def run(self, task: Task, snapshot: ContextSnapshot, budget: Budget) -> AgentResult:
        if task != self.workspace.task or task.claim.role != 'action':
            return AgentResult(task, 'failed', 0, 0, None, 'policy_denied', 0, None, (),
                (Event(1, 'run_started', 0), Event(2, 'run_failed', 0, error_kind='policy_denied')))
        result = Agent(self.provider).run(task, snapshot, self.workspace.tools(), budget)
        if result.status != 'completed':
            return result
        try:
            proposed = result.output
            validate(FINAL_SCHEMA, proposed)
            tests = [o for o in result.observations if o.name == 'action.go_test']
            if not tests:
                raise ValueError('no actual test')
            latest = tests[-1]
            observed = latest.output
            if (proposed['test_call_id'] != latest.call_id or not observed['passed'] or observed['exit_code'] != 0
                    or observed['workspace_sha256'] != self.workspace.workspace_sha
                    or proposed['workspace_sha256'] != observed['workspace_sha256']
                    or proposed['patch_id'] != observed['patch_id'] or proposed['patch_id'] != self.workspace.patch_id):
                raise ValueError('test claim differs from actual current execution')
            proposal = self.workspace.patch_proposal()
            identity = canonical_json({'run': task.claim.run_id, 'task': task.claim.task_id,
                'patch': proposal, 'execution': observed['output_sha256']})
            output = canonical_json({'schema_version': 'action_proposal.v1', 'summary': proposed['summary'],
                'patch': proposal, 'test_evidence': [o.output | {'call_id': o.call_id} for o in tests],
                'artifact_uri': 'artifact://' + str(uuid5(NAMESPACE_URL, identity)), 'persisted': False,
                'verified': False, 'input_snapshot_sha256': snapshot.content_sha256,
                'producer_run_id': task.claim.run_id, 'producer_task_id': task.claim.task_id,
                'source_plan_version': task.claim.plan_version})
        except (ValueError, ValidationError, KeyError, TypeError, PermissionError):
            event = Event(len(result.events) + 1, 'action_proposal_rejected', result.turns, error_kind='evidence_rejected')
            return replace(result, status='failed', output_json=None, error_kind='evidence_rejected', error_turn=result.turns,
                error_tool=None, events=result.events + (event, Event(len(result.events) + 2, 'run_failed', result.turns, error_kind='evidence_rejected')))
        event = Event(len(result.events) + 1, 'action_proposal_prepared', result.turns,
            observation_hash=hashlib.sha256(output.encode()).hexdigest())
        return replace(result, output_json=output, events=result.events + (event,))
