"""Read-only final activation/stop evidence; does not replay any invocation."""
from datetime import datetime, timezone
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'tests'))
from conftest import _admin_kwargs
import psycopg

journal = ROOT / 'evidence/eval/live/P7-06-deepseek-flash-1017-20261026'
summary = json.loads((journal / 'report.json').read_text())
kwargs = _admin_kwargs()
kwargs['options'] = '-c default_transaction_read_only=on -c statement_timeout=3000'
with psycopg.connect(**kwargs) as conn:
    marker = conn.execute('SELECT w.run_id,w.goal,w.state,w.revision,l.owner_id,l.epoch,l.revision FROM public.workflow_runs w JOIN public.orchestrator_leases l USING(run_id) WHERE w.run_id=%s',
                          ('92d7ff2b-cf3d-5ec3-a1a0-c938f24aa4a7',)).fetchone()
    assert marker and marker[2] == 'cancelled' and marker[3] == 1 and marker[5] == 2 and marker[6] == 1
    budget = conn.execute('SELECT to_jsonb(b) FROM public.run_budgets b WHERE run_id=%s', (marker[0],)).fetchone()[0]
    assert all(budget[k] == 0 for k in ['max_cost_usd','max_model_calls','max_tool_calls','reserved_model_calls','settled_model_calls','reserved_tool_calls','settled_tool_calls','reserved_cost','settled_cost'])
    events = conn.execute('SELECT kind,entity_revision FROM public.workflow_events WHERE run_id=%s ORDER BY entity_type,entity_revision,event_id', (marker[0],)).fetchall()
unknown = []
for i, record in enumerate(summary['records']):
    if record['status'] != 'UNKNOWN':
        continue
    raw = json.loads((journal / f'{i:03d}.raw.json').read_text())
    unknown.append({'index':i,'assignment':record['assignment'],'category':raw['case']['category'],
                    'budget':raw['result']['budget'],'unconfirmed_tools':[t for t in raw['tool_calls'] if t['status'] in ['reserved','unknown']],
                    'raw_path':str((journal / f'{i:03d}.raw.json').relative_to(ROOT))})
result = {'checked_at':datetime.now(timezone.utc).isoformat(),'read_only':True,
          'original_marker':dict(zip(['run_id','goal','state','workflow_revision','owner_id','epoch','lease_revision'],marker)),
          'marker_budget':budget,'marker_events':events,'executor':json.loads((journal / 'executor.json').read_text()),
          'stop_status':summary['status'],'unknown_assignments':unknown,'automatic_replay_performed':False}
out = Path(__file__).with_name('recovery-activation-and-stop.json')
assert not out.exists()
out.write_text(json.dumps(result,default=str,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({'marker_state':marker[2],'epoch':marker[5],'stop_status':summary['status'],'unknown_assignments':len(unknown),'replay':False}))
