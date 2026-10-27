"""Record the observed read-only fault classification without replaying it."""
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
NEW = ROOT / 'evidence/eval/live/P7-06-round2-deepseek-flash-1017-20261027'
OLD = ROOT / 'evidence/eval/live/P7-06-deepseek-flash-1017-20261026'
rows = []
for index in range(12, 16):
    path = NEW / f'{index:03d}.raw.json'
    raw = json.loads(path.read_text())
    record = json.loads((NEW / f'{index:03d}.result.json').read_text())
    budget = raw['result']['budget']
    failed = [tool for tool in raw['tool_calls'] if tool['capability'] == 'data.query']
    assert raw['case']['case_id'] == 'live-004' and raw['case']['category'] == 'task_failure'
    assert record['status'] == 'failed' and failed and all(tool['status'] == 'failed' and tool['effect'] == 'readonly' for tool in failed)
    assert all(budget[key] == 0 for key in ['unknown_models', 'pending_models', 'unknown_tools', 'pending_tools'])
    assert budget['conserved'] and record['known_cost_micros'] == record['upper_cost_micros']
    rows.append({'index': index, 'case_id': 'live-004', 'arm': raw['arm'], 'status': record['status'],
                 'data_query_statuses': [tool['status'] for tool in failed], 'unknown_models': 0,
                 'unknown_tools': 0, 'known_cost_micros': record['known_cost_micros'],
                 'raw_sha256': hashlib.sha256(path.read_bytes()).hexdigest()})
old = json.loads((OLD / '012.result.json').read_text())
assert old['status'] == 'UNKNOWN'
assert (NEW / '016.started.json').exists()
result = {'status': 'OBSERVED_AS_EXPECTED', 'old_live004_B0_status': old['status'],
          'round2_rows': rows, 'later_assignment_started': True, 'replayed': False,
          'limitation': 'One post-fix regression on exposed cases; this observation is not a causal effect estimate.'}
with Path(__file__).with_name('readonly-failure-regression.json').open('x') as stream:
    stream.write(json.dumps(result, indent=2) + '\n')
print(json.dumps(result))
