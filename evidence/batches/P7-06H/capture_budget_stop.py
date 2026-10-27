"""Summarize final round2 budget and stop evidence; never modifies a Run."""
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
plan = json.loads((ROOT / 'eval/live/round2/plan.json').read_text())
journal = ROOT / 'evidence/eval/live' / plan['experiment_id']
aggregate = json.loads((journal / 'report.json').read_text())
analysis = ROOT / 'evidence/eval/live/round2-results-20261027'
metrics = json.loads((analysis / 'metrics.json').read_text())
pg = json.loads(Path(__file__).with_name('retained-pg-reconciliation.json').read_text())
old = json.loads((ROOT / 'evidence/eval/live/actual-20261027/metrics.json').read_text())
commands = json.loads(Path(__file__).with_name('commands.json').read_text())
launch = [item for item in commands if item['log_path'].endswith('/frozen-round2-run.log')]
assert len(launch) == 1
assert metrics['known_cost_micros'] == aggregate['known_cost_micros'] == pg['known_cost_micros']
assert metrics['upper_cost_micros'] == aggregate['upper_cost_micros']
assert metrics['cumulative_upper_cost_micros'] == plan['previous']['upper_cost_micros'] + aggregate['upper_cost_micros'] <= 2000000
assert pg['status'] == 'MATCH' and pg['model_calls'] == metrics['recorded_model_calls']
statuses = dict(Counter(row['status'] for row in aggregate['records']))
unknown = []
for index, record in enumerate(aggregate['records']):
    if record['status'] != 'UNKNOWN':
        continue
    path = journal / f'{index:03d}.raw.json'
    raw = json.loads(path.read_text())
    budget = (raw.get('result') or {}).get('budget')
    models = [call for call in raw.get('model_calls') or [] if call['status'] in ['reserved', 'unknown']]
    tools = [tool for tool in raw.get('tool_calls') or [] if tool['status'] in ['reserved', 'unknown']]
    cause = 'unconfirmed_model_usage' if models else 'unconfirmed_tool_execution' if tools else 'incomplete_controller_or_ledger_evidence'
    unknown.append({'index': index, 'assignment': record['assignment'], 'reason': record['reason'],
                    'cause_class': cause, 'category': (raw.get('case') or {}).get('category'),
                    'budget': budget, 'unconfirmed_models': models, 'unconfirmed_tools': tools,
                    'controller_errors': (raw.get('result') or {}).get('errors'),
                    'raw_path': str(path.relative_to(ROOT)), 'raw_sha256': hashlib.sha256(path.read_bytes()).hexdigest()})
    assert all(row['status'] == 'NOT_RUN' for row in aggregate['records'][index + 1:])
attempted = 96 - statuses.get('NOT_RUN', 0)
assert len(list(journal.glob('*.started.json'))) == attempted
if aggregate['status'] == 'STOPPED_UNKNOWN':
    assert len(unknown) == 1
    reason = 'automatic_unknown_guard'
elif attempted == 96 and not unknown:
    reason = 'all_96_assignments_attempted'
else:
    reason = aggregate['status']
result = {'timestamp_utc': datetime.now(timezone.utc).isoformat(), 'experiment_id': plan['experiment_id'],
          'controller_status': aggregate['status'], 'execution_command_exit_code': launch[0]['exit_code'],
          'analysis_status': metrics['status'], 'global_stop_reason': reason, 'assignment_statuses': statuses,
          'assignments_attempted': attempted, 'assignments_evaluated': sum(row['evaluated'] for row in metrics['arms'].values()),
          'oracle_passed': sum(row['oracle_pass'] for row in metrics['arms'].values()),
          'real_model_closure_verified': metrics['real_model_closure_verified'],
          'recorded_model_calls': metrics['recorded_model_calls'], 'known_model_calls': metrics['known_model_calls'],
          'budget': {'unit': 'USD microdollars', 'project_cap_micros': 2000000, 'new_approved_cap_micros': 1900000,
                     'new_allocated_cap_micros': 96 * 19791, 'slot_cap_micros': 19791,
                     'previous_known_cost_micros': old['known_cost_micros'],
                     'previous_retained_upper_micros': plan['previous']['upper_cost_micros'],
                     'cumulative_approved_cap_micros': 1900000 + plan['previous']['upper_cost_micros'],
                     'new_known_cost_micros': aggregate['known_cost_micros'],
                     'new_reserved_ledger_cost_micros': pg['reserved_cost_micros'],
                     'new_retained_upper_cost_micros': aggregate['upper_cost_micros'],
                     'cumulative_known_cost_micros': old['known_cost_micros'] + aggregate['known_cost_micros'],
                     'cumulative_retained_upper_cost_micros': metrics['cumulative_upper_cost_micros'],
                     'project_cap_reached': metrics['cumulative_upper_cost_micros'] >= 2000000,
                     'estimate_is_supplier_invoice': False, 'unused_shares_reallocated': False},
          'unknown_stop_evidence': unknown, 'automatic_replay_performed': False,
          'analysis_report': str((analysis / 'report.md').relative_to(ROOT)),
          'postgres_reconciliation': 'evidence/batches/P7-06H/retained-pg-reconciliation.json',
          'limitations': ['Known costs use frozen worst-case prices, not a provider invoice.',
                         'The old unknown share remains retained; rounds are scored separately.',
                         'NOT_RUN is not an observed zero-cost task. No missing pair is dropped.',
                         'Post-fix regression on exposed cases is not an unseen holdout.']}
with Path(__file__).with_name('budget-and-stop.json').open('x') as stream:
    stream.write(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({key: value for key, value in result.items() if key not in ['unknown_stop_evidence', 'limitations']}))
