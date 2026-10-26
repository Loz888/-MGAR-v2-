"""Read-only check of the approved allocation before any generation."""
from datetime import datetime, timezone
import json
from pathlib import Path
import sys
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'tests'))
from conftest import _admin_kwargs
import psycopg

allocation = str(uuid.uuid5(uuid.NAMESPACE_URL, 'mgar/P7-06/deepseek/flash-1017/2026-10-26/allocation/all'))
kwargs = _admin_kwargs()
kwargs['options'] = '-c default_transaction_read_only=on -c statement_timeout=3000'
with psycopg.connect(**kwargs) as conn:
    rows = conn.execute('SELECT count(*) FROM workflow_runs WHERE run_id=%s', (allocation,)).fetchone()[0]
exists = (ROOT / 'evidence/eval/live/P7-06-deepseek-flash-1017-20261026').exists()
result = {'allocation_control_run_id': allocation, 'allocation_control_rows': rows,
          'formal_journal_exists': exists, 'timestamp_utc': datetime.now(timezone.utc).isoformat()}
print(json.dumps(result))
if rows or exists:
    raise SystemExit(1)
