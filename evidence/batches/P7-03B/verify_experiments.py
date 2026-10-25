"""Read-only cross-group checks for this batch's two formal experiment sets."""
import gzip
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT))
from eval.runner import verify_output, validate_complete  # noqa: E402

directories = [ROOT / 'evidence/experiments/p703b-main', ROOT / 'evidence/experiments/p703b-ablations']
cases = json.loads((ROOT / 'eval/cases/offline-60.json').read_text())
all_rows, database_namespaces, artifact_namespaces = [], set(), set()
counts = {'assignments': 0, 'negative_refusals': 0, 'budget_denials': 0,
          'retained_tool_failures': 0, 'duplicate_delivery_cases': 0,
          'accepted_partial_replans': 0, 'no_replan_failures': 0,
          'unverified_baseline_runs': 0, 'external_sandbox_checks': 0}
fingerprints = set()
for directory in directories:
    verify_output(directory)
    assignment = json.loads((directory / 'assignment.json').read_text())
    fingerprints.add(assignment['source_fingerprint'])
    assert assignment['full_offline_assignment'] and len(assignment['cases']) == 60
    for line in (directory / 'runs.jsonl').read_text().splitlines():
        row = json.loads(line)
        all_rows.append(row)
        assert row['infrastructure_error'] is None, row
        raw = json.loads(gzip.decompress((directory / row['raw']).read_bytes()))
        result = raw['result']
        assert raw['database_namespace'] not in database_namespaces
        assert raw['artifact_namespace'] not in artifact_namespaces
        database_namespaces.add(raw['database_namespace'])
        artifact_namespaces.add(raw['artifact_namespace'])
        assert result['source_unchanged'] and result['budget']['conserved']
        assert 'error' not in raw['execution_oracle'], raw['execution_oracle']
        assert result['trace_state'] == 'complete_projection', result['trace_state']
        counts['assignments'] += 1
        counts['external_sandbox_checks'] += raw['execution_oracle']['sandbox_executions']
        if not raw['treatment']['internal_verifier']:
            assert result['verdict_count'] == 0 and raw['workflow_success'] is None
            assert all(c['semantic_status'] == 'unverified' for c in result['candidates'])
            counts['unverified_baseline_runs'] += 1
        category = row['category']
        if category in ('refusal', 'policy_denied'):
            assert row['oracle_pass'] and row['execution_status'] == 'blocked'
            assert not raw['tool_calls'] and not raw['model_calls']
            counts['negative_refusals'] += 1
        elif category == 'budget_exhausted':
            assert not row['oracle_pass'] and not raw['model_calls'] and not raw['tool_calls']
            counts['budget_denials'] += 1
        elif category == 'task_failure':
            assert not row['oracle_pass'] and row['execution_status'] == 'failed'
            assert any(t['capability'] == 'data.query' and t['status'] == 'unknown' for t in raw['tool_calls'])
            counts['retained_tool_failures'] += 1
        elif category == 'reordered_duplicate':
            assert raw['duplicate_deliveries'] > 0 and row['oracle_pass']
            counts['duplicate_delivery_cases'] += 1
        elif category in ('partial_replan', 'stale_context'):
            assert len(raw['input_transitions']) >= 2
            if row['arm'] == 'B4':
                assert row['oracle_pass'] and len(raw['replans']) == 1
                assert raw['replans'][0]['accepted'] and raw['replans'][0]['decision']['reuse']
                counts['accepted_partial_replans'] += 1
            if row['arm'] == 'A1':
                assert not row['oracle_pass'] and not raw['replans']
                counts['no_replan_failures'] += 1
        if row['arm'] == 'B0' and row['execution_status'] == 'completed':
            assert len(result['workers']) == 1 and result['parallel_workers'] == 1

validate_complete(all_rows, [case['case_id'] for case in cases], ['B0', 'B1', 'B2', 'B3', 'B4', 'A1', 'A2'])
assert len(fingerprints) == 1
counts['source_fingerprint'] = fingerprints.pop()
counts['status'] = 'PASS'
print(json.dumps(counts, ensure_ascii=False, indent=2))
