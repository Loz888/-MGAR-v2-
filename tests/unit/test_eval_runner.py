"""Fairness and append-only failure retention are independent of outcomes."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile

import pytest

from eval.runner import append_record, load_config, validate_config, validate_complete
from eval import runner

ROOT = Path(__file__).resolve().parents[2]


def test_seven_arms_have_equal_authority_and_frozen_treatments():
    config = load_config(ROOT / 'eval/config/arms.yaml')
    assert set(config['arms']) == {'B0', 'B1', 'B2', 'B3', 'B4', 'A1', 'A2'}
    assert config['shared']['model'] == 'demo-mock.v1'
    assert config['shared']['core_tools'] == ['research.read', 'data.query', 'action.patch', 'action.test']
    assert config['arms']['B3'] == config['arms']['A2']


@pytest.mark.parametrize('mutation', ['tool', 'budget', 'paid', 'treatment', 'extra'])
def test_unfair_or_unknown_configuration_is_rejected(mutation):
    config = copy.deepcopy(load_config(ROOT / 'eval/config/arms.yaml'))
    if mutation == 'tool':
        config['shared']['core_tools'].remove('data.query')
    elif mutation == 'budget':
        config['arms']['B0']['budget_multiplier'] = 0.5
    elif mutation == 'paid':
        config['shared']['provider'] = 'live'
    elif mutation == 'treatment':
        config['arms']['B4']['internal_verifier'] = False
    else:
        config['arms']['B5'] = config['arms']['B4']
    with pytest.raises(ValueError):
        validate_config(config)


def test_failed_samples_are_required_and_not_overwritten(tmp_path):
    path = tmp_path / 'runs.jsonl'
    good = {'case_id': 'offline-001', 'arm': 'B0', 'execution_status': 'completed'}
    failed = {'case_id': 'offline-002', 'arm': 'B0', 'execution_status': 'failed'}
    append_record(path, good)
    append_record(path, failed)
    rows = [json.loads(line) for line in path.read_text().splitlines()]
    assert rows == [good, failed]
    validate_complete(rows, ['offline-001', 'offline-002'], ['B0'])
    with pytest.raises(ValueError):
        validate_complete(rows[:1], ['offline-001', 'offline-002'], ['B0'])
    with pytest.raises(ValueError):
        validate_complete(rows + rows[:1], ['offline-001', 'offline-002'], ['B0'])


def test_executor_initialization_failure_with_null_fields_is_retained(monkeypatch):
    real_run = subprocess.run

    def injected(args, **kwargs):
        if args[:2] == ['go', 'build']:
            return subprocess.CompletedProcess(args, 0)
        if str(args[0]).endswith('/mgareval'):
            raw = json.dumps({'answer': None, 'result': None, 'model_calls': None, 'tool_calls': None}).encode()
            return subprocess.CompletedProcess(args, 1, raw, b'fixture_initialization_failed\n')
        return real_run(args, **kwargs)

    monkeypatch.setattr(runner.subprocess, 'run', injected)
    base = ROOT / '.local/eval-runs'
    base.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=base, prefix='test-owned-') as temp:
        output = Path(temp) / 'out'
        assert runner.main(['--arms', 'B0', '--cases', 'offline-002', '--output', str(output)]) == 1
        rows = [json.loads(line) for line in (output / 'runs.jsonl').read_text().splitlines()]
        assert len(rows) == 1 and rows[0]['infrastructure_error'] == 'executor_nonzero'
        assert rows[0]['execution_status'] == 'failed' and not rows[0]['oracle_pass']
        assert runner.verify_output(output)['assigned'] == 1
        (output / 'oracle_judgments.jsonl').write_text('')
        with pytest.raises(ValueError, match='MISSING_OR_DUPLICATE_ASSIGNMENT'):
            runner.verify_output(output)
