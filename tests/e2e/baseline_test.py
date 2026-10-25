"""Isolated experiment completion never grants production verification."""
import json
from pathlib import Path
import subprocess

from eval.oracle.reference import judge
from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parents[2]


def execute(case, fault='none'):
    p = subprocess.run(['bash', 'scripts/baseline.sh', '--case', case, '--fault', fault], cwd=ROOT,
                       capture_output=True, text=True, timeout=240)
    assert p.returncode == 0, p.stderr
    return json.loads(p.stdout)


def test_baseline_analysis_and_wrong_answer_remain_unverified():
    data = json.loads((ROOT/'fixtures/datasets/v1/inputs.json').read_text())['offline-002']
    truth = json.loads((ROOT/'eval/oracle/v1_truth.json').read_text())['offline-002']
    for fault in ['none', 'wrong-data']:
        r = execute('offline-002', fault)
        assert r['execution_status'] == 'completed', r.get('errors')
        assert r['workflow_state'] == 'interrupted' and r['workflow_success'] is None
        assert r['semantic_status'] == 'unverified' and r['verdict_count'] == 0
        assert r['budget']['conserved'] and r['parallel_workers'] >= 2
        assert all(c['semantic_status'] == 'unverified' for c in r['candidates'])
        schema = json.loads((ROOT/'contracts/candidate_evidence.schema.json').read_text())
        validator = Draft202012Validator(schema, format_checker=FormatChecker())
        for candidate in r['candidates']:
            validator.validate(candidate)
            assert candidate['byte_length'] == len(candidate['content'].encode())
            assert not validator.is_valid(candidate | {'semantic_status': 'verified'})
        assert r['budget']['settled_tools'] == r['tools']
        assert judge(data, truth, r['answer'])['semantic_pass'] == (fault == 'none')


def test_baseline_incident_uses_actual_sandbox_once():
    r = execute('offline-001')
    assert r['execution_status'] == 'completed', r.get('errors')
    assert r['sandbox_executions'] == 1
    assert r['source_unchanged'] and r['verdict_count'] == 0
    assert r['workflow_success'] is None


def test_baseline_retains_denials_and_budget_failures():
    r = execute('offline-055')
    assert r['execution_status'] == 'blocked'
    assert r['answer']['reason'] == 'policy_denied'
    assert r['sandbox_executions'] == 0
    r = execute('offline-002', 'zero-tools')
    assert r['execution_status'] == 'failed' and r['errors']
    assert r['budget']['settled_tools'] == 0 and r['budget']['conserved']
