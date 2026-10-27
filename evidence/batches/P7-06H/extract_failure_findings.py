"""Extract observed failure shapes; do not infer hidden provider response text."""
from collections import Counter
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
journal = ROOT / 'evidence/eval/live/P7-06-round2-deepseek-flash-1017-20261027'
metrics = json.loads((ROOT / 'evidence/eval/live/round2-results-20261027/metrics.json').read_text())
worker_errors, run_errors, planner_states, workflows = Counter(), Counter(), Counter(), Counter()
aggregate_failures, semantic_negatives = [], []
for path in sorted(journal.glob('*.raw.json')):
    raw = json.loads(path.read_text())
    if raw['arm'] != 'B4':
        continue
    result = raw['result']
    run_errors[result.get('error_kind')] += 1
    workflows[result.get('state')] += 1
    for worker in result.get('workers') or []:
        if worker['role'] == 'planner':
            planner_states[worker['status']] += 1
        if worker['status'] != 'completed':
            worker_errors[worker['role'] + '/' + str(worker.get('error_kind'))] += 1
        if worker['role'] == 'aggregate' and worker.get('error_kind') == 'policy_denied':
            calls = raw.get('model_calls') or []
            tail = calls[-1]
            aggregate_failures.append({'case_id': raw['case']['case_id'], 'raw_path': str(path.relative_to(ROOT)),
                'worker_error': worker['error_kind'], 'turns': worker['turns'], 'tool_calls': worker['tool_calls'],
                'last_model_call_id': tail['call_id'], 'last_model_status': tail['status'],
                'last_output_tokens': tail['usage'].get('output_tokens'), 'usage_known': tail['usage_known']})
    scored = next(row for row in metrics['per_case'] if row['case_id'] == raw['case']['case_id'] and row['arm'] == 'B4')
    if scored['oracle_pass']:
        semantic_negatives.append({'case_id': raw['case']['case_id'], 'negative': raw['case']['negative'],
                                   'status': scored['status'], 'strict_closure': scored['real_model_closure_verified']})
assert len(aggregate_failures) == 13
assert all(row['last_output_tokens'] == 1024 and row['last_model_status'] == 'failed' and row['usage_known'] for row in aggregate_failures)
assert all(row['negative'] and row['status'] == 'blocked' and not row['strict_closure'] for row in semantic_negatives)
result = {'status': 'OBSERVED', 'B4_run_errors': dict(run_errors), 'B4_worker_errors': dict(worker_errors),
          'B4_planner_states': dict(planner_states), 'B4_workflow_states': dict(workflows),
          'aggregate_failures': aggregate_failures, 'B4_oracle_passed_negative_cases': semantic_negatives,
          'strict_closure_count': 0,
          'interpretation_limit': 'Output at the configured ceiling and a failed model record are observed. The discarded provider response text/finish reason is not retained; truncation and the exact contract mismatch require development reproduction. Generic worker policy_denied is not proof of a tool permission violation.'}
with Path(__file__).with_name('failure-findings.json').open('x') as stream:
    stream.write(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({key: value for key, value in result.items() if key not in ['aggregate_failures', 'B4_oracle_passed_negative_cases']}))
