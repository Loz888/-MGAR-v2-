"""Read-only reconciliation of the frozen round2 journal and retained PG."""
from collections import Counter
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import sys
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'tests'))
from conftest import _admin_kwargs
import psycopg
from psycopg import sql


def main():
    plan = json.loads((ROOT / 'eval/live/round2/plan.json').read_text())
    journal = ROOT / 'evidence/eval/live' / plan['experiment_id']
    aggregate = json.loads((journal / 'report.json').read_text())
    ids = [item['run_id'] for item in plan['assignments']]
    marker_id = str(uuid.uuid5(uuid.NAMESPACE_URL, 'mgar/P7-06/deepseek/flash-1017/2026-10-27/round2/allocation/all'))
    kwargs = _admin_kwargs()
    kwargs['options'] = '-c default_transaction_read_only=on -c statement_timeout=5000'
    result = {'checked_at': datetime.now(timezone.utc).isoformat(), 'read_only': True,
              'experiment_id': plan['experiment_id'], 'assignments': []}
    fields = ['call_id', 'logical_call_id', 'run_id', 'task_id', 'provider', 'model', 'status',
              'usage', 'usage_known', 'usage_source', 'reservation', 'settlement']
    seen_models, seen_tools, seen_runs = set(), set(), set()
    with psycopg.connect(**kwargs) as conn:
        conn.execute('SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY')
        marker = conn.execute('SELECT w.state,w.revision,l.epoch,l.revision FROM public.workflow_runs w JOIN public.orchestrator_leases l USING(run_id) WHERE w.run_id=%s', (marker_id,)).fetchone()
        # A fresh CreateRunClaimed starts epoch=1, revision=0. Only the old
        # one-time recovery advanced its lease revision to 1.
        assert marker == ('cancelled', 1, 1, 0), 'allocation_marker_mismatch'
        marker_budget = conn.execute('SELECT to_jsonb(b) FROM public.run_budgets b WHERE run_id=%s', (marker_id,)).fetchone()[0]
        assert all(marker_budget[k] == 0 for k in ['max_cost_usd', 'max_model_calls', 'max_tool_calls', 'reserved_model_calls', 'settled_model_calls', 'reserved_tool_calls', 'settled_tool_calls', 'reserved_cost', 'settled_cost'])
        old = conn.execute('SELECT w.state,l.epoch FROM public.workflow_runs w JOIN public.orchestrator_leases l USING(run_id) WHERE run_id=%s', ('92d7ff2b-cf3d-5ec3-a1a0-c938f24aa4a7',)).fetchone()
        assert old == ('cancelled', 2), 'old_marker_changed'
        result.update(allocation_marker={'run_id': marker_id, 'state': marker[0], 'workflow_revision': marker[1], 'epoch': marker[2], 'lease_revision': marker[3], 'budget_zero': True}, old_marker_state=old[0], old_marker_epoch=old[1])
        for path in sorted(journal.glob('*.raw.json')):
            raw = json.loads(path.read_text())
            namespace = raw.get('database_namespace')
            if not namespace:
                result['assignments'].append({'raw': path.name, 'status': 'NO_COMPLETE_NAMESPACE'})
                continue
            assert re.fullmatch(r'mgar_demo_[0-9a-f]{16}', namespace), 'invalid_owned_namespace'
            assert raw['run_id'] in ids and raw['run_id'] not in seen_runs, 'invalid_run_identity'
            seen_runs.add(raw['run_id'])
            query = sql.SQL('SELECT to_jsonb(m) FROM {}.model_calls m WHERE run_id=%s').format(sql.Identifier(namespace))
            models = [row[0] for row in conn.execute(query, (raw['run_id'],))]
            actual = {m['call_id']: {key: m[key] for key in fields} for m in models}
            exported = {m['call_id']: {key: m[key] for key in fields} for m in raw.get('model_calls') or []}
            assert actual == exported and len(actual) == len(models), 'retained_model_ledger_mismatch'
            assert not seen_models.intersection(actual), 'duplicate_model_id'
            seen_models.update(actual)
            query = sql.SQL("SELECT jsonb_build_object('invocation_id',v.invocation_id,'run_id',t.run_id,'task_id',v.task_id,'capability',v.capability,'request_hash',v.request_hash,'effect',v.effect_class,'tool_version',v.scope->>'Version','plan_version',v.scope->'Plan','status',v.status,'result_artifact_id',v.result_artifact_id) FROM {}.tool_invocations v JOIN {}.task_instances t USING(task_id) WHERE t.run_id=%s").format(sql.Identifier(namespace), sql.Identifier(namespace))
            tools = {row[0]['invocation_id']: row[0] for row in conn.execute(query, (raw['run_id'],))}
            exported_tools = {item['invocation_id']: item for item in raw.get('tool_calls') or []}
            assert tools == exported_tools, 'retained_tool_ledger_mismatch'
            assert not seen_tools.intersection(tools), 'duplicate_tool_id'
            seen_tools.update(tools)
            query = sql.SQL('SELECT max_cost_usd,settled_cost,reserved_cost FROM {}.run_budgets WHERE run_id=%s').format(sql.Identifier(namespace))
            cap, settled, reserved = conn.execute(query, (raw['run_id'],)).fetchone()
            budget = raw['result']['budget']
            assert (int(cap * 1000000), int(settled * 1000000), int(reserved * 1000000)) == (19791, budget['settled_cost_micros'], budget['reserved_cost_micros']), 'retained_budget_mismatch'
            result['assignments'].append({'raw': path.name, 'run_id': raw['run_id'], 'namespace': namespace,
                'model_calls': len(models), 'tool_calls': len(tools),
                'model_statuses': dict(Counter(item['status'] for item in models)),
                'tool_statuses': dict(Counter(item['status'] for item in tools.values())),
                'known_cost_micros': int(settled * 1000000), 'reserved_cost_micros': int(reserved * 1000000), 'status': 'MATCH'})
        namespaces = [r[0] for r in conn.execute("SELECT nspname FROM pg_namespace WHERE nspname='public' OR nspname~'^mgar_demo_[0-9a-f]{16}$' ORDER BY nspname")]
        all_models, all_tools, all_runs = set(), set(), set()
        for namespace in namespaces:
            for table, column, target in [('model_calls', 'call_id', all_models), ('workflow_runs', 'run_id', all_runs)]:
                query = sql.SQL('SELECT {}::text FROM {} WHERE run_id::text=ANY(%s)').format(sql.Identifier(column), sql.Identifier(namespace, table))
                target.update(row[0] for row in conn.execute(query, (ids,)))
            query = sql.SQL('SELECT v.invocation_id::text FROM {} v JOIN {} t USING(task_id) WHERE t.run_id::text=ANY(%s)').format(sql.Identifier(namespace, 'tool_invocations'), sql.Identifier(namespace, 'task_instances'))
            all_tools.update(row[0] for row in conn.execute(query, (ids,)))
        assert all_models == seen_models and all_tools == seen_tools and all_runs == seen_runs, 'unexported_round2_rows'
    assert len(result['assignments']) == len(list(journal.glob('*.started.json'))), 'started_export_missing'
    result.update(status='MATCH' if all(row['status'] == 'MATCH' for row in result['assignments']) else 'PARTIAL',
                  checked_assignments=len(result['assignments']), model_calls=len(seen_models), tool_calls=len(seen_tools),
                  no_unexported_round2_rows=True, experiment_status=aggregate['status'],
                  known_cost_micros=sum(row.get('known_cost_micros', 0) for row in result['assignments']),
                  reserved_cost_micros=sum(row.get('reserved_cost_micros', 0) for row in result['assignments']))
    assert result['known_cost_micros'] == aggregate['known_cost_micros'], 'aggregate_known_cost_changed'
    out = Path(__file__).with_name('retained-pg-reconciliation.json')
    with out.open('x') as stream:
        stream.write(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps({key: value for key, value in result.items() if key != 'assignments'}))
    return 0 if result['status'] == 'MATCH' else 2


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (AssertionError, psycopg.Error, OSError, ValueError, KeyError, TypeError) as error:
        print(json.dumps({'status': 'RECONCILIATION_ERROR', 'error_type': type(error).__name__,
                          'assertion': str(error) if isinstance(error, AssertionError) else 'details_withheld'}))
        raise SystemExit(1)
