"""Data-role loop: numeric claims must bind to actual readonly observations."""
from __future__ import annotations

from dataclasses import replace
import hashlib
from typing import Any
from uuid import NAMESPACE_URL, uuid5

from jsonschema.exceptions import ValidationError

from mgar_agents.core.agent import Agent
from mgar_agents.core.provider import Provider
from mgar_agents.core.schemas import AgentResult, Budget, ContextSnapshot, Event, Task, canonical_json, validate

from .reader import SQLReader

FINAL_SCHEMA: dict[str, Any] = {'type': 'object', 'additionalProperties': False, 'required': ['summary', 'metrics'],
    'properties': {'summary': {'type': 'string', 'minLength': 1, 'maxLength': 4096, 'pattern': r'\S'},
        'metrics': {'type': 'array', 'minItems': 1, 'maxItems': 16, 'items': {'type': 'object', 'additionalProperties': False,
            'required': ['call_id', 'row', 'column', 'value'], 'properties': {
                'call_id': {'type': 'string', 'minLength': 1, 'maxLength': 64},
                'row': {'type': 'integer', 'minimum': 0, 'maximum': 99},
                'column': {'type': 'string', 'minLength': 1, 'maxLength': 63},
                'value': {'type': ['string', 'number', 'boolean', 'null']}}}}}}


class DataAgent:
    def __init__(self, provider: Provider, reader: SQLReader) -> None:
        self.provider = provider
        self.reader = reader

    def run(self, task: Task, snapshot: ContextSnapshot, budget: Budget) -> AgentResult:
        if task.claim.role != 'data':
            return AgentResult(task, 'failed', 0, 0, None, 'policy_denied', 0, None, (),
                (Event(1, 'run_started', 0), Event(2, 'run_failed', 0, error_kind='policy_denied')))
        result = Agent(self.provider).run(task, snapshot, self.reader.tools(), budget)
        if result.status != 'completed':
            return result
        try:
            proposed = result.output
            validate(FINAL_SCHEMA, proposed)
            observed = {item.call_id: item.output for item in result.observations if item.name == 'data.query'}
            seen: set[tuple[str, int, str]] = set()
            for metric in proposed['metrics']:
                source = observed.get(metric['call_id'])
                if source is None or metric['row'] >= len(source['rows']):
                    raise ValueError('numeric claim has no observation')
                names = [col['name'] for col in source['columns']]
                if metric['column'] not in names:
                    raise ValueError('numeric claim names an absent column')
                actual = source['rows'][metric['row']][names.index(metric['column'])]
                if type(metric['value']) is not type(actual) or metric['value'] != actual:
                    raise ValueError('numeric claim differs from actual typed SQL value')
                identity = (metric['call_id'], metric['row'], metric['column'])
                if identity in seen:
                    raise ValueError('duplicate metric')
                seen.add(identity)
            evidence: list[dict[str, Any]] = []
            for call_id, source in observed.items():
                candidate = str(uuid5(NAMESPACE_URL, canonical_json({'run': task.claim.run_id, 'task': task.claim.task_id,
                    'call': call_id, 'source': source['content_sha256']})))
                evidence.append({**source, 'call_id': call_id, 'artifact_uri': 'artifact://' + candidate,
                    'persisted': False, 'producer_run_id': task.claim.run_id, 'producer_task_id': task.claim.task_id,
                    'source_plan_version': task.claim.plan_version})
            output = canonical_json({'schema_version': 'data_finding.v1', 'summary': proposed['summary'],
                'metrics': proposed['metrics'], 'evidence': evidence, 'verified': False,
                'input_snapshot_sha256': snapshot.content_sha256, 'producer_run_id': task.claim.run_id,
                'producer_task_id': task.claim.task_id, 'source_plan_version': task.claim.plan_version})
        except (ValueError, ValidationError, KeyError, TypeError, IndexError):
            event = Event(len(result.events) + 1, 'data_finding_rejected', result.turns, error_kind='evidence_rejected')
            return replace(result, status='failed', output_json=None, error_kind='evidence_rejected', error_turn=result.turns,
                error_tool=None, events=result.events + (event, Event(len(result.events) + 2, 'run_failed', result.turns, error_kind='evidence_rejected')))
        event = Event(len(result.events) + 1, 'data_finding_prepared', result.turns, observation_hash=hashlib.sha256(output.encode()).hexdigest())
        return replace(result, output_json=output, events=result.events + (event,))
