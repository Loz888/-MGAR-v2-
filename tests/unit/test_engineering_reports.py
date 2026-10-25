"""Missing windows, lost allocations and skipped tests cannot become evidence."""
import copy

import pytest

from eval.load.runner import guard, validate_kills, validate_load


def test_eight_windows_need_matching_death_and_recovery():
    kills = [{'point': 'F-' + c, 'signal': 'SIGKILL', 'owned_pid': i + 100, 'process_group': i + 100,
              'before': {'run_id': 'run-' + c}, 'after_death': {'run_id': 'run-' + c}} for i, c in enumerate('ABCDEFGH')]
    recoveries = [{'point': 'F-' + c, 'run_id': 'run-' + c} for c in 'ABCDEFGH']
    assert len(validate_kills(kills, recoveries)) == 8
    for mode in ['missing', 'duplicate', 'wrong_signal', 'wrong_run', 'unsafe_group']:
        bad = copy.deepcopy(kills)
        if mode == 'missing':
            bad.pop()
        elif mode == 'duplicate':
            bad[-1] = bad[0]
        elif mode == 'wrong_signal':
            bad[0]['signal'] = 'SIGTERM'
        elif mode == 'wrong_run':
            bad[0]['after_death']['run_id'] = 'unrelated'
        else:
            bad[0]['process_group'] = 1
        with pytest.raises(ValueError):
            validate_kills(bad, recoveries)
    with pytest.raises(ValueError):
        validate_kills(kills, recoveries[:-1])


def test_load_does_not_accept_missing_or_duplicate_concurrency_levels():
    for levels in [[], [1, 4, 8], [1, 4, 8, 8]]:
        with pytest.raises(ValueError):
            validate_load([{'concurrency': value} for value in levels])


def test_load_refuses_paid_mode_and_low_memory(monkeypatch):
    monkeypatch.setenv('MGAR_ALLOW_PAID', '1')
    with pytest.raises(ValueError, match='OFFLINE_REQUIRED'):
        guard({})
    monkeypatch.setenv('MGAR_ALLOW_PAID', '0')
    with pytest.raises(ValueError, match='HEADROOM'):
        guard({'memory.max': str(1024**3), 'memory.current': str(512 * 1024**2)})


def test_long_go_evidence_frames_reassemble_without_cross_test_mix():
    from eval.load.runner import parse_records
    events = [{'Test': 'a', 'Output': 'file.go:10: LOAD_REPORT {"assigned":'},
              {'Test': 'b', 'Output': 'unrelated\n'},
              {'Test': 'a', 'Output': '64}\n'}]
    assert parse_records(events, 'LOAD_REPORT') == [{'assigned': 64}]


@pytest.fixture
def observed_load():
    import json
    from eval.runner import ROOT
    report = ROOT / 'evidence/eval/engineering/load-529f1d9fe6cb434f83e12a7d9a9fc010/report.json'
    return json.loads(report.read_text())['raw_measurements']


def test_observed_load_denominators_units_and_identity(observed_load):
    from eval.analysis.metrics import percentile
    summary = validate_load(observed_load)
    assert sum(row['assigned'] for row in summary) == 116
    for row, raw in zip(summary, observed_load, strict=True):
        assert row['claim_to_output_p95_ms'] == percentile([o['latency_ms'] for o in raw['worker_observations']], 95)
        assert row['worker_execution_tasks_per_second'] == pytest.approx(raw['completed'] * 1000 / raw['execution_ms'])


@pytest.mark.parametrize('mode', ['lost_failure', 'duplicate_task', 'missing_worker', 'capacity', 'budget', 'nan_latency'])
def test_load_rejects_missing_or_forged_evidence(observed_load, mode):
    data = copy.deepcopy(observed_load)
    r = data[-1]
    if mode == 'lost_failure':
        r['completed'] -= 1
    elif mode == 'duplicate_task':
        r['worker_observations'][-1] = r['worker_observations'][0]
    elif mode == 'missing_worker':
        r['worker_observations'][0]['pid'] = r['worker_observations'][-1]['pid']
    elif mode == 'capacity':
        r['samples'][0]['max_class_held'] = r['resource_capacity'] + 1
    elif mode == 'budget':
        r['workflows'][0]['budget']['conserved'] = False
    else:
        r['worker_observations'][0]['latency_ms'] = float('nan')
    with pytest.raises(ValueError):
        validate_load(data)
