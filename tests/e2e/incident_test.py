"""Full control-plane acceptance; model decisions are explicitly offline mocks."""
import hashlib
import json
from pathlib import Path
import subprocess

import pytest

from eval.oracle.reference import judge

ROOT = Path(__file__).resolve().parents[2]


def run_demo(case='offline-001', fault='none'):
    result = subprocess.run(['bash', 'scripts/demo.sh', '--provider', 'mock', '--case', case, '--fault', fault],
                            cwd=ROOT, text=True, capture_output=True, timeout=240)
    assert result.returncode == 0, result.stdout + result.stderr
    report = json.loads(result.stdout)
    assert report['budget']['conserved']
    assert report['budget']['pending_models'] == 0
    assert report['budget']['settled_tools'] == report['tools']
    assert report['source_unchanged']
    assert report['audit']['events']
    assert all(e['TraceMatches'] for e in report['audit']['events'])
    return report


def assert_success(report, case, action):
    assert report['state'] == 'succeeded', report.get('error_kind')
    assert report['provider'] == 'mock'
    assert report['trace_state'] == 'complete_projection'
    assert report['otlp_trace']['resourceSpans']
    assert report['plan_version'] == 2
    data = json.loads((ROOT/'fixtures/datasets/v1/inputs.json').read_text())[case]
    truth = json.loads((ROOT/'eval/oracle/v1_truth.json').read_text())[case]
    assert judge(data, truth, report['answer'])['semantic_pass']
    assert report['parallel_workers'] >= 2
    assert len({w['pid'] for w in report['workers']}) >= 4
    assert all(w['status'] == 'completed' and not w['verified'] for w in report['workers'])
    assert report['tools'] >= (5 if action else 3)
    assert report['sandbox_executions'] >= (2 if action else 0)
    assert report['audit']['handoffs']
    artifacts = {a['metadata']['artifact_id']: a for a in report['artifacts']}
    for fact in report['merge']['facts']:
        assert fact['verified'] and fact['sources']
        for source in fact['sources']:
            item = artifacts[source['artifact_id']]
            assert source['content_sha256'] == hashlib.sha256(item['body'].encode()).hexdigest()
            assert source['source_plan_version'] == item['metadata']['source_plan_version'] == 2
            assert item['verdict']['status'] == 'passed'
    subprocess.run([str(ROOT/'.venv/bin/python'), 'eval/oracle/validate_manifest.py'], cwd=ROOT, check=True, capture_output=True)


def test_incident_full_control_plane():
    assert_success(run_demo(), 'offline-001', True)


@pytest.mark.parametrize('role', ['planner', 'research', 'data', 'action', 'aggregate'])
def test_fabricated_role_cannot_succeed(role):
    report = run_demo(fault='forge-' + role)
    assert report['state'] != 'succeeded'
    assert report['error_kind']


def test_missing_evidence_cannot_succeed():
    report = run_demo(case='offline-049')
    assert report['state'] == 'blocked'
    assert report['answer']['reason'] == 'insufficient_evidence'


def test_bad_patch_cannot_claim_passing_tests():
    report = run_demo(fault='wrong-patch')
    assert report['state'] == 'failed'
    assert any(a['verdict']['status'] == 'failed' for a in report['artifacts'])
