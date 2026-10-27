"""Read-only round2 preparation audit; never activates an allocation or model."""
from datetime import datetime, timezone
from pathlib import Path
import hashlib
import json
import sys
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'tests'))
from conftest import _admin_kwargs
import psycopg
from psycopg import sql

plan_path = ROOT / 'eval/live/round2/plan.json'
plan = json.loads(plan_path.read_text())
ids = [a['run_id'] for a in plan['assignments']]
allocation = str(uuid.uuid5(uuid.NAMESPACE_URL, 'mgar/P7-06/deepseek/flash-1017/2026-10-27/round2/allocation/all'))
kwargs = _admin_kwargs()
kwargs['options'] = '-c default_transaction_read_only=on -c statement_timeout=3000'
with psycopg.connect(**kwargs) as conn:
    marker = conn.execute('SELECT count(*) FROM public.workflow_runs WHERE run_id=%s', (allocation,)).fetchone()[0]
    assert marker == 0
    names = [r[0] for r in conn.execute("SELECT nspname FROM pg_namespace WHERE nspname='public' OR nspname~'^mgar_demo_[0-9a-f]{16}$' ORDER BY nspname")]
    models = 0
    for namespace in names:
        models += conn.execute(sql.SQL('SELECT count(*) FROM {} WHERE run_id::text=ANY(%s)').format(
            sql.Identifier(namespace, 'model_calls')), (ids,)).fetchone()[0]
    assert models == 0
    old = conn.execute('SELECT w.state,l.epoch FROM public.workflow_runs w JOIN public.orchestrator_leases l USING(run_id) WHERE run_id=%s',
                       ('92d7ff2b-cf3d-5ec3-a1a0-c938f24aa4a7',)).fetchone()
    assert old == ('cancelled', 2)
prior = ROOT / 'evidence/eval/live' / plan['previous']['experiment_id'] / 'report.json'
assert hashlib.sha256(prior.read_bytes()).hexdigest() == plan['previous']['report_sha256']
assert not (ROOT / 'evidence/eval/live' / plan['experiment_id']).exists()
result = {'checked_at':datetime.now(timezone.utc).isoformat(),'read_only':True,'round':'round2',
          'plan_sha256':hashlib.sha256(plan_path.read_bytes()).hexdigest(),'allocation_run_id':allocation,
          'allocation_marker_rows':marker,'model_calls':models,'assigned':len(ids),'status':'PREPARED_NOT_RUN',
          'new_cap_micros':plan['approved_cap_micros'],'previous_upper_cost_micros':plan['previous']['upper_cost_micros'],
          'cumulative_approved_cap_micros':plan['approved_cap_micros']+plan['previous']['upper_cost_micros'],
          'allocated_micros':sum(a['cap_micros'] for a in plan['assignments']),
          'old_marker_state':old[0],'old_marker_epoch':old[1],'prior_report_hash_matches':True}
out = Path(__file__).with_name('prepared-state.json')
assert not out.exists()
out.write_text(json.dumps(result,indent=2)+'\n')
print(json.dumps(result))
