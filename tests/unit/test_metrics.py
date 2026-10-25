"""Statistics must preserve denominators, pairing, units and missing usage."""
import math

import pytest

from eval.analysis.metrics import paired_delta, percentile, rate, summarize, usage_summary


def test_rates_keep_failures_and_zero_denominators():
    assert rate(2, 5) == 0.4
    assert rate(0, 0) is None
    with pytest.raises(ValueError):
        rate(2, 1)
    rows = [{'oracle_pass': value, 'success_claim': claim, 'workflow_success': None,
             'latency_ms': latency, 'workflow_ms': None, 'models': [], 'tool_calls': 0,
             'critical_admitted': 0, 'critical_available': 0, 'citation_valid': 0,
             'citation_total': 0, 'business_roles_retained': 0, 'business_roles_required': 0}
            for value, claim, latency in [(True, True, 1000), (False, True, 3000), (False, False, 5000)]]
    result = summarize(rows)
    assert result['assigned'] == 3 and result['task_success_rate'] == 1 / 3
    assert result['unsupported_answer_claim_rate'] == 0.5
    assert result['workflow_success_rate'] is None
    assert result['experiment_latency_p50_ms'] == 3000
    assert result['critical_evidence_retention'] is None
    assert summarize([])['task_success_rate'] is None


def test_percentiles_are_sorted_linear_and_keep_milliseconds():
    assert percentile([30, 0, 20, 10], 50) == 15
    assert percentile([30, 0, 20, 10], 95) == pytest.approx(28.5)
    assert percentile([], 95) is None
    assert percentile([900], 95) == 900
    for bad in [[math.nan], [math.inf], [-1], [True]]:
        with pytest.raises(ValueError):
            percentile(bad, 95)


def test_pairing_is_reproducible_and_never_intersects_away_missing_cases():
    left, right = {'a': 0, 'b': 1}, {'b': 1, 'a': 1}
    a = paired_delta(left, right, seed=17)
    assert a == paired_delta(dict(reversed(list(left.items()))), right, seed=17)
    assert a['delta'] == 0.5 and a['ci95'] == [0.0, 1.0] and a['pairs'] == 2
    assert a['resamples'] == 5000
    assert paired_delta({}, {})['delta'] is None
    with pytest.raises(ValueError):
        paired_delta(left, {'a': 1})


def test_unknown_usage_is_not_free_and_uses_explicit_upper_bounds():
    known = {'usage_known': True, 'usage_source': 'local_stub',
             'usage': {'input_tokens': 10, 'output_tokens': 5, 'cost_micros': 100},
             'reservation': {'input_tokens': 20, 'output_tokens': 30, 'cost_micros': 200}}
    unknown = {'usage_known': False, 'usage': {},
               'reservation': {'input_tokens': 40, 'output_tokens': 60, 'cost_micros': 900}}
    r = usage_summary([known, unknown])
    assert r['coverage'] == 0.5
    assert r['input_tokens'] is None and r['output_tokens'] is None and r['estimated_cost_usd'] is None
    assert r['input_tokens_upper'] == 50 and r['output_tokens_upper'] == 65
    assert r['estimated_cost_usd_upper'] == 0.001
    assert usage_summary([unknown | {'reservation': {}}])['input_tokens_upper'] is None
    assert usage_summary([])['input_tokens'] == 0 and usage_summary([])['coverage'] is None


def test_claimed_known_but_missing_or_invalid_usage_fails_closed():
    r = usage_summary([{'usage_known': True, 'usage': {'input_tokens': 5}, 'reservation': {}}])
    assert r['coverage'] == 0 and r['input_tokens'] is None
    with pytest.raises(ValueError):
        usage_summary([{'usage_known': True, 'usage': {'input_tokens': -1, 'output_tokens': 1, 'cost_micros': 0}}])


@pytest.fixture
def analysis_workspace():
    import tempfile
    from pathlib import Path
    from eval.runner import ROOT
    with tempfile.TemporaryDirectory(prefix='analysis-test-', dir=ROOT / '.local') as directory:
        yield Path(directory)


def test_full_raw_report_reproducible_preserves_420_cases(analysis_workspace):
    import hashlib
    import json
    from eval.analysis.report import generate
    from eval.runner import ROOT
    source = ROOT / 'evidence/eval/offline'
    first, second = analysis_workspace / 'first', analysis_workspace / 'second'
    report = generate(source, first)
    generate(source, second)
    def hashes(folder):
        return {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in folder.iterdir()}
    assert hashes(first) == hashes(second)
    assert report['assigned'] == 420 and len(report['per_case']) == 420
    assert all(s['assigned'] == 60 and s['oracle_fail'] > 0 for s in report['arms'].values())
    assert len(report['categories']) == 70
    assert report['arms']['B0']['critical_evidence_retention'] is None
    assert report['arms']['B4']['critical_evidence_retention'] is None
    assert report['arms']['B4']['citation_identity_validity'] == 1
    assert report['arms']['B4']['business_role_coverage'] == 1
    assert report['arms']['B1']['workflow_success_rate'] is None
    assert all(p['pairs'] == 60 for p in report['paired'])
    assert 'N/A' in (first / 'by_arm.csv').read_text()
    assert json.loads((first / 'metrics.json').read_text())['paid_calls'] == 0
    with pytest.raises(ValueError, match='OUTPUT_ALREADY_EXISTS'):
        generate(source, first)


@pytest.mark.parametrize('mutation', ['hash', 'missing_group', 'duplicate_group', 'inventory'])
def test_frozen_inputs_reject_tampering_and_missing_pairs(analysis_workspace, mutation):
    import json
    from eval.analysis.inputs import load
    from eval.runner import ROOT
    index = json.loads((ROOT / 'evidence/eval/offline/index.json').read_text())
    if mutation == 'hash':
        index['groups'][0]['files_sha256']['runs.jsonl'] = '0' * 64
    elif mutation == 'missing_group':
        index['groups'].pop()
    elif mutation == 'duplicate_group':
        index['groups'].append(index['groups'][0])
    else:
        del index['groups'][0]['files_sha256']['runs.jsonl']
    (analysis_workspace / 'index.json').write_text(json.dumps(index))
    with pytest.raises(ValueError):
        load(analysis_workspace)


def test_paths_cannot_escape_or_follow_symlink(analysis_workspace):
    from eval.analysis.inputs import local_path
    for path in ['/tmp/unrelated-report', analysis_workspace / '..' / 'escape']:
        with pytest.raises(ValueError):
            local_path(path)
    link = analysis_workspace / 'link'
    link.symlink_to(analysis_workspace, target_is_directory=True)
    with pytest.raises(ValueError):
        local_path(link / 'report')


def test_final_reference_identity_and_body_cannot_be_forged():
    import gzip
    import json
    from eval.analysis.inputs import information
    from eval.runner import ROOT
    report = json.loads(gzip.decompress((ROOT / 'evidence/experiments/p703b-main/raw/offline-001-B4.json.gz').read_bytes()))
    baseline = information(report, {'business': 'incident'})
    report['result']['merge']['facts'][0]['sources'][0]['content_sha256'] = '0' * 64
    forged = information(report, {'business': 'incident'})
    assert forged['citation_total'] == baseline['citation_total']
    assert forged['citation_valid'] == baseline['citation_valid'] - 1
