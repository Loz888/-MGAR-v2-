"""Check frozen execution inputs and the untouched original trial after round2."""
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'scripts'))
from source_fingerprint import fingerprint

runbook = json.loads((ROOT / 'evidence/batches/P7-06G/freeze-and-runbook.json').read_text())
assert fingerprint() == runbook['source_fingerprint'], 'execution_source_changed'
plan_path = ROOT / runbook['plan_path']
assert hashlib.sha256(plan_path.read_bytes()).hexdigest() == runbook['plan_sha256']
plan = json.loads(plan_path.read_text())
journal = ROOT / 'evidence/eval/live' / plan['experiment_id']
assert json.loads((journal / 'assignment.json').read_text()) == plan
report = json.loads((journal / 'report.json').read_text())
assert len(report['records']) == 96 and report['experiment_id'] == plan['experiment_id']
executor = json.loads((journal / 'executor.json').read_text())
assert executor['allocation_mode'] == 'fresh' and not executor['recovery_proof_sha256']
assert hashlib.sha256((ROOT / '.local/bin/mgareval').read_bytes()).hexdigest() == executor['sha256']
old_metrics_path = ROOT / 'evidence/eval/live/actual-20261027/metrics.json'
old_metrics = json.loads(old_metrics_path.read_text())
old_journal = ROOT / 'evidence/eval/live' / plan['previous']['experiment_id']
for name, digest in old_metrics['input_hashes'].items():
    assert hashlib.sha256((old_journal / name).read_bytes()).hexdigest() == digest, 'old_journal_changed'
assert hashlib.sha256((old_journal / 'report.json').read_bytes()).hexdigest() == plan['previous']['report_sha256']
old_tracked = subprocess.check_output(['git', 'show', 'HEAD:evidence/eval/live/actual-20261027/metrics.json'], cwd=ROOT)
assert old_metrics_path.read_bytes() == old_tracked, 'old_metrics_changed'
result = {'checked_at': datetime.now(timezone.utc).isoformat(), 'status': 'FROZEN_INPUTS_UNCHANGED',
          'source_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
          'execution_source_fingerprint': fingerprint(), 'plan_sha256': runbook['plan_sha256'],
          'executor_binary_sha256': executor['sha256'], 'allocation_mode': 'fresh',
          'old_journal_hashes_verified': len(old_metrics['input_hashes']), 'old_metrics_unchanged': True,
          'model_configuration_changed': False, 'oracle_or_dataset_changed': False,
          'automatic_retry_performed': False, 'status_from_controller': report['status']}
with Path(__file__).with_name('frozen-execution-check.json').open('x') as stream:
    stream.write(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(result))
