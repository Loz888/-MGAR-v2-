"""Read-only migration/allocation audit; never prints credentials or sends HTTP."""
from datetime import datetime, timezone
from decimal import Decimal
import hashlib
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[3]
output_name = sys.argv[1] if len(sys.argv) == 2 else 'schema-before-repair.json'
if len(sys.argv) > 2 or output_name not in {'schema-before-repair.json', 'schema-after-repair.json'}:
    raise SystemExit('Expected an approved audit output filename')
sys.path.insert(0, str(ROOT / 'tests'))
from conftest import _admin_kwargs
import psycopg
from psycopg import sql

plan = json.loads((ROOT / 'eval/live/plan.json').read_text())
assignments = plan['assignments']
run_ids = [a['run_id'] for a in assignments]
allocation = '92d7ff2b-cf3d-5ec3-a1a0-c938f24aa4a7'
kwargs = _admin_kwargs()
kwargs['options'] = '-c default_transaction_read_only=on -c statement_timeout=5000'
result = {'timestamp_utc': datetime.now(timezone.utc).isoformat(), 'read_only': True,
          'plan_sha256': hashlib.sha256((ROOT / 'eval/live/plan.json').read_bytes()).hexdigest(),
          'formal_journal_exists': (ROOT / 'evidence/eval/live' / plan['experiment_id']).exists(),
          'allocation_control_run_id': allocation, 'namespaces': [], 'assignments': assignments}
with psycopg.connect(**kwargs) as conn:
    migrations = conn.execute('SELECT version,checksum FROM public.schema_migrations ORDER BY version').fetchall()
    result['public_migrations'] = [{'version': version, 'checksum': sha, 'source_checksum_matches':
        (ROOT / 'migrations' / (version + '.sql')).exists() and hashlib.sha256((ROOT / 'migrations' / (version + '.sql')).read_bytes()).hexdigest() == sha} for version, sha in migrations]
    result['public_budget_columns'] = [r[0] for r in conn.execute("SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name='run_budgets' ORDER BY ordinal_position")]
    marker = conn.execute('SELECT state,revision,active_plan_version,finished_at FROM public.workflow_runs WHERE run_id=%s', (allocation,)).fetchall()
    result['allocation_marker_rows'] = [{'state': r[0], 'revision': r[1], 'active_plan_version': r[2], 'finished_at': r[3]} for r in marker]
    result['allocation_budget_rows'] = [r[0] for r in conn.execute('SELECT to_jsonb(b) FROM public.run_budgets b WHERE run_id=%s', (allocation,))]
    result['allocation_model_calls'] = conn.execute('SELECT count(*) FROM public.model_calls WHERE run_id=%s', (allocation,)).fetchone()[0]
    result['allocation_tasks'] = conn.execute('SELECT count(*) FROM public.task_instances WHERE run_id=%s', (allocation,)).fetchone()[0]
    names = [r[0] for r in conn.execute("SELECT nspname FROM pg_namespace WHERE nspname='public' OR nspname ~ '^mgar_demo_[0-9a-f]{16}$' ORDER BY nspname")]
    for name in names:
        if name != 'public' and not re.fullmatch(r'mgar_demo_[0-9a-f]{16}', name):
            raise SystemExit('Unexpected synthetic namespace')
        tables = {r[0] for r in conn.execute('SELECT tablename FROM pg_tables WHERE schemaname=%s', (name,))}
        rows = []
        for assignment in assignments:
            run_id = assignment['run_id']
            row = {'run_id': run_id, 'case_id': assignment['case_id'], 'arm': assignment['arm']}
            for field, table in [('workflow_rows', 'workflow_runs'), ('tasks', 'task_instances'), ('model_calls', 'model_calls')]:
                row[field] = (conn.execute(sql.SQL('SELECT count(*) FROM {} WHERE run_id=%s').format(sql.Identifier(name, table)), (run_id,)).fetchone()[0]
                              if table in tables else 0)
            row['tool_calls'] = (conn.execute(sql.SQL('SELECT count(*) FROM {} v JOIN {} t USING(task_id) WHERE t.run_id=%s').format(
                sql.Identifier(name, 'tool_invocations'), sql.Identifier(name, 'task_instances')), (run_id,)).fetchone()[0]
                if {'tool_invocations', 'task_instances'} <= tables else 0)
            rows.append(row)
        result['namespaces'].append({'schema': name, 'present_tables': sorted(tables), 'assignments': rows})
result['assignment_totals'] = {key: sum(row[key] for ns in result['namespaces'] for row in ns['assignments'])
                               for key in ['workflow_rows', 'tasks', 'model_calls', 'tool_calls']}
output = Path(__file__).with_name(output_name)
if output.exists():
    raise SystemExit('Audit output already exists; refusing overwrite')
output.write_text(json.dumps(result, ensure_ascii=False, indent=2, default=lambda value: str(value) if isinstance(value, Decimal) else value.isoformat()) + '\n')
print(json.dumps({'audit_path': str(output.relative_to(ROOT)), 'namespaces': len(result['namespaces']),
                  'public_migrations': [r['version'] for r in result['public_migrations']],
                  'assignment_totals': result['assignment_totals'], 'formal_journal_exists': result['formal_journal_exists'],
                  'allocation_marker_rows': result['allocation_marker_rows'],
                  'allocation_model_calls': result['allocation_model_calls'], 'allocation_tasks': result['allocation_tasks']}, default=str))
