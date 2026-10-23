"""P2-04 model-driven analysis against actual, independently read-only PostgreSQL."""
from dataclasses import replace
import hashlib
import json
from uuid import uuid4

import pytest

from mgar_agents.core.claim import Claim
from mgar_agents.core.provider import MockProvider
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task, canonical_json
from mgar_agents.data import DataAgent, ReaderLimits, SQLReader


def data_task(role='data'):
    claim = Claim(str(uuid4()), str(uuid4()), 'fixture-data-worker', role, 1, 0, 1)
    return Task(claim, 'Investigate duplicate orders and calculate paid total', 'incident-data'), ContextSnapshot.from_dict({
        'schema_version': 'context.v1', 'task_id': claim.task_id, 'plan_version': 1,
        'model_config_version': 'mock.v1', 'tool_policy_version': 'readonly_sql.v1', 'artifacts': []})


def call(turn, statement, parameters=None):
    return json.dumps({'type': 'tool_call', 'call_id': f'query-{turn}', 'name': 'data.query',
                      'arguments': {'sql': statement, 'parameters': [] if parameters is None else parameters}})


def finish(summary, metrics):
    return json.dumps({'type': 'final', 'output': {'summary': summary, 'metrics': metrics}})


def test_data_real_sql_observations_drive_next_turn_and_numeric_finding(data_fixture):
    schema = data_fixture.schema
    def decide(request):
        if request.turn == 1:
            return call(1, f'SELECT COUNT(*) AS rows_seen, COUNT(DISTINCT order_id) AS unique_orders, SUM(amount) AS raw_total FROM {schema}.orders')
        if request.turn == 2:
            first = request.observations[0].output
            assert first['rows'] == [[4, 3, '320.00']]
            return call(2, f'SELECT order_id, COUNT(*) AS copies, MAX(amount) AS amount FROM {schema}.orders WHERE status = %s GROUP BY order_id ORDER BY order_id', ['paid'])
        second = request.observations[-1].output
        assert second['rows'] == [[101, 2, '100.00'], [102, 1, '50.00']]
        return finish('Repeated paid order 101 explains overcount; rows are actual database observations.', [
            {'call_id': 'query-1', 'row': 0, 'column': 'raw_total', 'value': '320.00'},
            {'call_id': 'query-2', 'row': 0, 'column': 'copies', 'value': 2},
            {'call_id': 'query-2', 'row': 1, 'column': 'amount', 'value': '50.00'}])
    provider = MockProvider(decide)
    task, snapshot = data_task()
    result = DataAgent(provider, data_fixture.reader).run(task, snapshot, Budget())
    assert result.status == 'completed' and result.turns == 3 and result.tool_calls == 2
    assert result.verified is False and result.output['verified'] is False
    assert result.output['input_snapshot_sha256'] == snapshot.content_sha256
    assert len(result.output['evidence']) == 2
    for evidence in result.output['evidence']:
        assert evidence['snapshot_id'] and evidence['row_count'] == len(evidence['rows'])
        assert evidence['fixture_version'] == 'incident-orders.v1'
        assert len(evidence['fixture_sha256']) == 64 and evidence['parameters'] in ([], ['paid'])
        assert evidence['artifact_uri'].startswith('artifact://') and evidence['persisted'] is False
        assert evidence['producer_run_id'] == task.claim.run_id and evidence['source_plan_version'] == 1
    assert result.output['evidence'][0]['rows'][0][2] == '320.00'
    assert result.events[-1].kind == 'data_finding_prepared'
    assert result.events[-1].observation_hash == hashlib.sha256(result.output_json.encode()).hexdigest()


def test_data_normal_paid_aggregation_matches_ground_truth(data_fixture):
    observed = data_fixture.reader.query(f'SELECT order_id, MAX(amount) AS amount FROM {data_fixture.schema}.orders WHERE status=%s GROUP BY order_id ORDER BY order_id', ['paid'], timeout_s=1)
    assert observed['rows'] == [[101, '100.00'], [102, '50.00']]
    assert sum(int(row[1].split('.')[0]) for row in observed['rows']) == 150
    assert observed['columns'][1]['type_oid'] == 1700
    saved = dict(observed)
    digest = saved.pop('content_sha256')
    assert digest == hashlib.sha256(canonical_json(saved).encode()).hexdigest()


@pytest.mark.parametrize('mutation', ['wrong_value', 'unknown_call', 'unknown_column', 'boolean_integer', 'verified'])
def test_data_rejects_forged_numeric_evidence(data_fixture, mutation):
    def decide(request):
        if request.turn == 1:
            return call(1, f'SELECT COUNT(*) AS n FROM {data_fixture.schema}.orders')
        metric = {'call_id': 'query-1', 'row': 0, 'column': 'n', 'value': 4}
        out = {'summary': 'unverified summary', 'metrics': [metric]}
        if mutation == 'wrong_value':
            metric['value'] = 999
        elif mutation == 'unknown_call':
            metric['call_id'] = 'not-observed'
        elif mutation == 'unknown_column':
            metric['column'] = 'fake'
        elif mutation == 'boolean_integer':
            metric['value'] = True
        else:
            out['verified'] = True
        return json.dumps({'type': 'final', 'output': out})
    task, snapshot = data_task()
    result = DataAgent(MockProvider(decide), data_fixture.reader).run(task, snapshot, Budget())
    assert result.status == 'failed' and result.error_kind == 'evidence_rejected'
    assert result.output is None and result.verified is False


def test_data_role_cannot_be_substituted(data_fixture):
    task, snapshot = data_task('research')
    p = MockProvider([finish('fake', [])])
    result = DataAgent(p, data_fixture.reader).run(task, snapshot, Budget())
    assert result.error_kind == 'policy_denied' and p.requests == []


def test_data_row_and_output_caps_are_real(data_fixture):
    limited = SQLReader(data_fixture.credentials, data_fixture.policy, 'incident-orders.v1', data_fixture.reader.fixture_sha256, replace(ReaderLimits(), max_rows=2))
    with pytest.raises(PermissionError):
        limited.query(f'SELECT order_id FROM {data_fixture.schema}.orders', [], timeout_s=1)
    tiny = SQLReader(data_fixture.credentials, data_fixture.policy, 'incident-orders.v1', data_fixture.reader.fixture_sha256, replace(ReaderLimits(), max_output_bytes=64))
    with pytest.raises(PermissionError):
        tiny.query(f'SELECT status FROM {data_fixture.schema}.orders', [], timeout_s=1)


def test_data_statement_deadline_cancels_actual_postgresql_lock_wait(data_fixture):
    reader = SQLReader(data_fixture.credentials, data_fixture.policy, 'incident-orders.v1', data_fixture.reader.fixture_sha256,
                       replace(ReaderLimits(), statement_timeout_ms=30, lock_timeout_ms=200))
    with data_fixture.lock_orders():
        with pytest.raises(TimeoutError):
            reader.query(f'SELECT order_id FROM {data_fixture.schema}.orders', [], timeout_s=1)
