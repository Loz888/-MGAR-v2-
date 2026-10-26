"""Read only retained synthetic PG; verify formal journal exports against it."""
from collections import Counter
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'tests'))
from conftest import _admin_kwargs
import psycopg
from psycopg import sql


def main():
    journal = ROOT / 'evidence/eval/live/P7-06-deepseek-flash-1017-20261026'
    kwargs = _admin_kwargs()
    kwargs['options'] = '-c default_transaction_read_only=on -c statement_timeout=5000'
    result = {'checked_at': datetime.now(timezone.utc).isoformat(), 'read_only': True, 'assignments': []}
    fields = ['call_id', 'logical_call_id', 'run_id', 'task_id', 'provider', 'model', 'status',
              'usage', 'usage_known', 'usage_source', 'reservation', 'settlement']
    with psycopg.connect(**kwargs) as conn:
        marker = conn.execute('SELECT state FROM public.workflow_runs WHERE run_id=%s',
                              ('92d7ff2b-cf3d-5ec3-a1a0-c938f24aa4a7',)).fetchall()
        assert marker == [('cancelled',)], 'allocation_marker_mismatch'
        result['allocation_marker'] = 'cancelled'
        for p in sorted(journal.glob('*.raw.json')):
            raw = json.loads(p.read_text())
            namespace = raw.get('database_namespace')
            if not namespace:
                result['assignments'].append({'raw': p.name, 'status': 'NO_COMPLETE_NAMESPACE'})
                continue
            assert re.fullmatch(r'mgar_demo_[0-9a-f]{16}', namespace), 'invalid_owned_namespace'
            query = sql.SQL('SELECT to_jsonb(m) FROM {}.model_calls m WHERE run_id=%s').format(sql.Identifier(namespace))
            models = [row[0] for row in conn.execute(query, (raw['run_id'],))]
            actual = {m['call_id']: {k: m[k] for k in fields} for m in models}
            exported = {m['call_id']: {k: m[k] for k in fields} for m in raw['model_calls']}
            assert actual == exported, 'retained_model_ledger_mismatch'
            query = sql.SQL('SELECT status,count(*) FROM {}.tool_invocations WHERE run_id=%s GROUP BY status').format(sql.Identifier(namespace))
            tools = dict(conn.execute(query, (raw['run_id'],)))
            assert tools == dict(Counter(t['status'] for t in raw['tool_calls'])), 'retained_tool_ledger_mismatch'
            query = sql.SQL('SELECT max_cost_usd,settled_cost,reserved_cost FROM {}.run_budgets WHERE run_id=%s').format(sql.Identifier(namespace))
            cap, settled, reserved = conn.execute(query, (raw['run_id'],)).fetchone()
            budget = raw['result']['budget']
            assert (int(cap*1000000), int(settled*1000000), int(reserved*1000000)) == (
                20833,budget['settled_cost_micros'],budget['reserved_cost_micros']), 'retained_budget_mismatch'
            result['assignments'].append({'raw': p.name,'run_id':raw['run_id'],'namespace':namespace,
                'model_calls':len(models),'tool_calls':sum(tools.values()),'known_cost_micros':int(settled*1000000),
                'reserved_cost_micros':int(reserved*1000000),'status':'MATCH'})
    result['status'] = 'MATCH' if all(r['status']=='MATCH' for r in result['assignments']) else 'PARTIAL'
    out = ROOT / 'evidence/batches/P7-06C/retained-pg-reconciliation.json'
    assert not out.exists(), 'output_already_exists'
    out.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps({'status':result['status'],'checked_assignments':len(result['assignments']),
                     'model_calls':sum(r.get('model_calls',0) for r in result['assignments'])}))
    return 0 if result['status']=='MATCH' else 2


if __name__=='__main__':
    try:
        raise SystemExit(main())
    except (AssertionError, psycopg.Error, OSError, ValueError, KeyError, TypeError) as error:
        print(json.dumps({'status':'RECONCILIATION_ERROR','error_type':type(error).__name__}))
        raise SystemExit(1)
