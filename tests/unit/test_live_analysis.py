"""Synthetic analyzer tests; never contact a provider or claim live evidence."""
import json
import hashlib
from pathlib import Path
import shutil

import pytest

from eval.live.analyze import analyze, audit_call
from eval.runner import KEYS, TREATMENTS
from eval.live.tokenizer_data import OFFICIAL_TEXT_BOUND, proof_fingerprint

ROOT = Path(__file__).resolve().parents[2]


def test_live_absent_journal_retains_96_not_run(tmp_path):
    report = analyze(tmp_path / 'absent')
    assert report['status'] == 'BLOCKED'
    assert len(report['per_case']) == 96
    assert all(row['status'] == 'NOT_RUN' and row['oracle_pass'] is None for row in report['per_case'])
    assert report['real_model_closure_verified'] is False
    assert all(row['delta'] is None for row in report['paired'])
    assert report['estimated_cost_usd'] is None


def test_live_changed_assignment_rejected(tmp_path):
    plan = json.loads((ROOT / 'eval/live/plan.json').read_text())
    plan['assignments'].pop()
    (tmp_path / 'assignment.json').write_text(json.dumps(plan))
    with pytest.raises(ValueError, match='ASSIGNMENT'):
        analyze(tmp_path)


def test_live_crashed_assignment_remains_unknown(tmp_path):
    plan = json.loads((ROOT / 'eval/live/plan.json').read_text())
    (tmp_path / 'assignment.json').write_text(json.dumps(plan))
    (tmp_path / '000.started.json').write_text(json.dumps(plan['assignments'][0]))
    r = analyze(tmp_path)
    assert r['per_case'][0]['status'] == 'UNKNOWN'
    assert r['per_case'][0]['upper_cost_micros'] == 20833
    assert sum(row['status'] == 'NOT_RUN' for row in r['per_case']) == 95
    assert not r['real_model_closure_verified']


def test_live_corrupt_raw_hash_rejected(tmp_path):
    plan = json.loads((ROOT / 'eval/live/plan.json').read_text())
    assignment = plan['assignments'][0]
    for name, value in [('assignment.json', plan), ('000.started.json', assignment),
                        ('000.result.json', {'assignment': assignment, 'raw_sha256': '0' * 64})]:
        (tmp_path / name).write_text(json.dumps(value))
    (tmp_path / '000.raw.json').write_text('{}')
    with pytest.raises(ValueError, match='RAW_HASH'):
        analyze(tmp_path)


def fixture_call():
    price = {'provider': 'deepseek', 'model': 'flash-1017', 'version': 'synthetic.v1',
             'input_micros_per_million': 1_000_000, 'output_micros_per_million': 1_000_000}
    call = {'call_id': 'synthetic-call', 'run_id': 'synthetic-run', 'provider': 'deepseek',
            'model': 'flash-1017', 'status': 'succeeded', 'usage_known': True, 'usage_source': 'provider',
            'reservation': {'pricing': price, 'pricing_version': 'synthetic.v1', 'input_digest': 'a' * 64,
                            'input_tokens': 40, 'output_tokens': 20, 'tokens': 60, 'cost_micros': 60},
            'usage': {'input_tokens': 10, 'output_tokens': 5, 'tokens': 15, 'cost_micros': 15,
                      'pricing_version': 'synthetic.v1', 'response_model': 'flash-1017', 'response_sha256': 'b' * 64}}
    return call, price


def test_live_accounting_uses_frozen_integer_prices():
    call, price = fixture_call()
    assert audit_call(call, price, 'synthetic-run') == (15, 15, True)


@pytest.mark.parametrize('fault', ['local_stub', 'price', 'cost', 'identity', 'usage', 'hash'])
def test_live_forged_accounting_rejected(fault):
    call, price = fixture_call()
    if fault == 'local_stub':
        call['usage_source'] = 'local_stub'
    elif fault == 'price':
        call['reservation']['pricing'] = price | {'version': 'rewritten'}
    elif fault == 'cost':
        call['usage']['cost_micros'] = 0
    elif fault == 'identity':
        call['run_id'] = 'another-run'
    elif fault == 'usage':
        call['usage']['input_tokens'] = True
    else:
        call['usage']['response_sha256'] = 'invalid'
    with pytest.raises(ValueError):
        audit_call(call, price, 'synthetic-run')


def test_live_unknown_is_not_zero_usage():
    call, price = fixture_call()
    call.update(status='unknown', usage_known=False, usage_source='unknown', usage={})
    assert audit_call(call, price, 'synthetic-run') == (None, 60, False)


@pytest.mark.parametrize('fault', ['', 'proof', 'price', 'input', 'output', 'missing'])
def test_live_byte_bound_is_linked_to_frozen_quote_and_envelope(fault):
    call, price = fixture_call()
    price['version'] += '.u8-' + proof_fingerprint(OFFICIAL_TEXT_BOUND)[:16]
    call['reservation']['pricing_version'] = price['version']
    call['usage']['pricing_version'] = price['version']
    bound = {'proof': OFFICIAL_TEXT_BOUND.copy(), 'maximum_input_tokens': 40, 'output_tokens': 20}
    if fault == 'proof':
        bound['proof']['tokenizer_sha256'] = '0' * 64
    elif fault == 'price':
        price['version'] = 'unlinked.v1'
        call['reservation']['pricing_version'] = price['version']
        call['usage']['pricing_version'] = price['version']
    elif fault == 'input':
        bound['maximum_input_tokens'] = 39
    elif fault == 'output':
        bound['output_tokens'] = 19
    elif fault == 'missing':
        bound = None
    if fault:
        with pytest.raises(ValueError):
            audit_call(call, price, 'synthetic-run', bound=bound)
    else:
        assert audit_call(call, price, 'synthetic-run', bound=bound) == (15, 15, True)


def synthetic_journal(tmp_path, count=96):
    """Synthetic contract data in pytest tmp only; NOT a live evidence artifact."""
    root, journal = tmp_path / 'root', tmp_path / 'journal'
    manifest = json.loads((ROOT / 'eval/dataset_manifest.json').read_text())
    for name in [*manifest['files'], 'eval/dataset_manifest.json', 'eval/live/plan.json']:
        dest = root / name
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(ROOT / name, dest)
    journal.mkdir()
    plan = json.loads((root / 'eval/live/plan.json').read_text())
    truth = json.loads((root / 'eval/oracle/v1_truth.json').read_text())
    cases = {c['case_id']: c for c in manifest['cases']}

    def save(name, value):
        (journal / name).write_text(json.dumps(value) + '\n')

    official = b'{"test_fixture_only":true}\n'
    (root / 'eval/live/official-evidence.json').write_bytes(official)
    _, price = fixture_call()
    save('assignment.json', plan)
    save('executor.json', {'sha256': 'c' * 64, 'started_at': '2026-10-26T00:00:00Z'})
    save('preflight.json', {'ready': True, 'blockers': [], 'cap_micros': 2000000,
         'allocated_micros': 96 * 20833, 'assignments': plan['assignments'], 'pricing': {
             'status': 'VERIFIED', 'config': {'price': price, 'official_evidence_sha256': hashlib.sha256(official).hexdigest()},
             'pricing_url': 'https://api-docs.deepseek.com/quick_start/pricing', 'models_url': 'https://api.deepseek.com/models'}})
    for i, a in enumerate(plan['assignments'][:count]):
        call, _ = fixture_call()
        call.update(call_id=f'synthetic-{i}', run_id=a['run_id'])
        c = cases[a['case_id']]
        required = c['business'] == 'incident' and not c['negative']
        oracle = {'version': 'experiment-execution-oracle.v1', 'required': required, 'passed': True}
        if required:
            oracle.update(sandbox_executions=1, source_sha256='d' * 64, observation={'exit_code': 0, 'tests': 1})
        budget = {'run_id': a['run_id'], 'conserved': True, 'limits': {
                  'cost_micros': 20833, 'model_calls': c['limits']['model_calls'], 'tool_calls': 64,
                  'tokens': 65536, 'concurrent_calls': 4, 'task_concurrency': 4, 'delegations': 32},
                  'settled_models': 1, 'reserved_models': 0, 'settled_tokens': 15, 'reserved_tokens': 0,
                  'settled_cost_micros': 15, 'reserved_cost_micros': 0, 'unknown_models': 0,
                  'pending_models': 0, 'settled_tools': 0, 'reserved_tools': 0,
                  'pending_tools': 0, 'unknown_tools': 0}
        artifacts = []
        for kind in ['finding', 'sql_evidence', 'report', 'patch']:
            artifact_id = f'synthetic-artifact-{i}-{kind}'
            artifacts.append({'body': '{}', 'metadata': {'artifact_id': artifact_id, 'producer_run_id': a['run_id'],
                'kind': kind, 'content_sha256': hashlib.sha256(b'{}').hexdigest(), 'byte_length': 2},
                'verdict': {'schema_version': 'verdict.v1', 'artifact_id': artifact_id, 'status': 'passed',
                            'verifier_version': 'verifier.v1', 'checks': [{'name': 'provenance', 'status': 'passed'}]}})
        reported_case = {k: c[k] for k in ['case_id', 'category', 'seed', 'negative', 'limits']}
        reported_case['limits'] = c['limits'] | {'cost_micros': 20833}
        if c['limits']['model_calls'] == 0:
            budget.update(settled_models=0, settled_tokens=0, settled_cost_micros=0)
        cost = budget['settled_cost_micros']
        raw = {'schema_version': 'experiment.v1', 'run_id': a['run_id'], 'arm': a['arm'], 'case': reported_case,
               'treatment': dict(zip(KEYS, TREATMENTS[a['arm']], strict=True)),
               'execution_status': 'completed', 'experiment_elapsed_ms': 10.0,
               'model_calls': [call] if cost else [], 'tool_calls': [], 'answer': truth[c['case_id']], 'execution_oracle': oracle,
               'workflow_success': True if a['arm'] == 'B4' else None,
               'result': {'state': 'succeeded', 'budget': budget, 'verdict_count': 0, 'artifacts': artifacts if a['arm'] == 'B4' else []}}
        save(f'{i:03d}.started.json', a)
        save(f'{i:03d}.raw.json', raw)
        save(f'{i:03d}.result.json', {'assignment': a, 'status': 'completed', 'known_cost_micros': cost,
             'upper_cost_micros': cost, 'raw_sha256': hashlib.sha256((journal / f'{i:03d}.raw.json').read_bytes()).hexdigest()})
    return root, journal


def rewrite_raw(journal, index, mutate):
    path = journal / f'{index:03d}.raw.json'
    raw = json.loads(path.read_text())
    mutate(raw)
    path.write_text(json.dumps(raw))
    result = journal / f'{index:03d}.result.json'
    record = json.loads(result.read_text())
    record['raw_sha256'] = hashlib.sha256(path.read_bytes()).hexdigest()
    result.write_text(json.dumps(record))


def test_live_analyzer_synthetic_full_contract(tmp_path):
    root, journal = synthetic_journal(tmp_path)
    report = analyze(journal, root=root)
    assert report['status'] == 'EVALUATED'
    assert report['known_cost_micros'] == report['upper_cost_micros'] == report['known_model_calls'] * 15
    assert report['real_model_closure_verified']
    assert all(row['pairs'] == 24 and row['delta'] == 0 for row in report['paired'])


def test_live_no_intersection_pair_dropping(tmp_path):
    root, journal = synthetic_journal(tmp_path, count=4)
    report = analyze(journal, root=root)
    assert report['status'] == 'BLOCKED'
    assert report['estimated_cost_usd'] is None
    assert all(row['pairs'] == 0 and row['missing_pairs'] == 23 for row in report['paired'])


@pytest.mark.parametrize('fault', ['wrong_answer', 'no_verdict', 'oracle_failed', 'replay_failed'])
def test_live_self_claim_cannot_prove_closure(tmp_path, fault):
    root, journal = synthetic_journal(tmp_path, count=4)

    def mutate(raw):
        if fault == 'wrong_answer':
            raw['answer'] = {'verified': True}
        elif fault == 'no_verdict':
            raw['result']['artifacts'] = []
        elif fault == 'oracle_failed':
            raw['execution_oracle']['passed'] = False
        else:
            raw['execution_oracle']['observation']['exit_code'] = 1
    rewrite_raw(journal, 3, mutate)
    assert not analyze(journal, root=root)['real_model_closure_verified']


@pytest.mark.parametrize('fault', ['duplicate', 'ledger', 'baseline', 'artifact'])
def test_live_cross_evidence_inconsistency_rejected(tmp_path, fault):
    root, journal = synthetic_journal(tmp_path, count=4)

    def mutate(raw):
        if fault == 'duplicate':
            raw['model_calls'][0]['call_id'] = 'synthetic-0'
        elif fault == 'ledger':
            raw['result']['budget']['settled_cost_micros'] = 0
        elif fault == 'baseline':
            raw['workflow_success'] = True
        else:
            raw['result']['artifacts'][0]['body'] = 'tampered'
    rewrite_raw(journal, 1 if fault == 'baseline' else 3, mutate)
    reason = {'duplicate': 'DUPLICATE_CALL', 'ledger': 'LEDGER_RECONCILIATION',
              'baseline': 'BASELINE_VERIFICATION_ELEVATED', 'artifact': 'ARTIFACT_PROVENANCE'}[fault]
    with pytest.raises(ValueError, match=reason):
        analyze(journal, root=root)


def test_live_nonsequential_starts_rejected(tmp_path):
    root, journal = synthetic_journal(tmp_path, count=0)
    plan = json.loads((journal / 'assignment.json').read_text())
    (journal / '001.started.json').write_text(json.dumps(plan['assignments'][1]))
    with pytest.raises(ValueError, match='NONSEQUENTIAL'):
        analyze(journal, root=root)


def test_live_duplicate_json_keys_rejected(tmp_path):
    (tmp_path / 'assignment.json').write_text('{"assignments":[],"assignments":[]}')
    with pytest.raises(ValueError, match='DUPLICATE_JSON'):
        analyze(tmp_path)


def test_live_unknown_ledger_keeps_share_and_stops(tmp_path):
    root, journal = synthetic_journal(tmp_path, count=1)

    def mutate(raw):
        raw['model_calls'][0].update(status='unknown', usage={}, usage_known=False, usage_source='conservative_bound')
        raw['result']['budget'].update(settled_models=0, reserved_models=1, unknown_models=1,
                                      settled_tokens=0, reserved_tokens=60, settled_cost_micros=0, reserved_cost_micros=60)
    rewrite_raw(journal, 0, mutate)
    result = journal / '000.result.json'
    result.write_text(json.dumps(json.loads(result.read_text()) | {
        'status': 'UNKNOWN', 'known_cost_micros': 0, 'upper_cost_micros': 20833}))
    report = analyze(journal, root=root)
    assert report['per_case'][0]['oracle_pass'] is None
    assert report['known_cost_micros'] == 0 and report['upper_cost_micros'] == 20833
    assert report['estimated_cost_usd'] is None


def test_live_known_failed_call_still_costs(tmp_path):
    root, journal = synthetic_journal(tmp_path, count=1)

    def mutate(raw):
        raw['model_calls'][0]['status'] = 'failed'
        raw.update(execution_status='failed', answer={})
    rewrite_raw(journal, 0, mutate)
    result = journal / '000.result.json'
    result.write_text(json.dumps(json.loads(result.read_text()) | {'status': 'failed'}))
    report = analyze(journal, root=root)
    assert report['per_case'][0]['oracle_pass'] is False
    assert report['known_cost_micros'] == 15


@pytest.mark.parametrize('state', ['unknown', 'reserved'])
def test_live_unfinished_tool_cannot_be_known_result(tmp_path, state):
    root, journal = synthetic_journal(tmp_path, count=1)

    def mutate(raw):
        raw['tool_calls'] = [{'run_id': raw['run_id'], 'tool_version': 'demo.v1',
                             'invocation_id': 'synthetic-tool', 'status': state}]
        raw['result']['budget'].update(reserved_tools=1, unknown_tools=int(state == 'unknown'),
                                      pending_tools=int(state == 'reserved'))
    rewrite_raw(journal, 0, mutate)
    with pytest.raises(ValueError, match='UNFINISHED_TOOL_ACCOUNTING'):
        analyze(journal, root=root)


@pytest.mark.parametrize('state', ['unknown', 'reserved'])
def test_live_unfinished_tool_unknown_preserves_known_model_cost(tmp_path, state):
    root, journal = synthetic_journal(tmp_path, count=1)

    def mutate(raw):
        raw['tool_calls'] = [{'run_id': raw['run_id'], 'tool_version': 'demo.v1',
                             'invocation_id': 'synthetic-tool', 'status': state}]
        raw['result']['budget'].update(reserved_tools=1, unknown_tools=int(state == 'unknown'),
                                      pending_tools=int(state == 'reserved'))
    rewrite_raw(journal, 0, mutate)
    result = journal / '000.result.json'
    result.write_text(json.dumps(json.loads(result.read_text()) | {'status': 'UNKNOWN', 'upper_cost_micros': 20833}))
    report = analyze(journal, root=root)
    assert report['per_case'][0]['oracle_pass'] is None
    assert report['known_cost_micros'] == 15 and report['upper_cost_micros'] == 20833
    assert sum(row['status'] == 'NOT_RUN' for row in report['per_case']) == 95


def test_live_zero_output_go_omitempty_is_valid_known_usage():
    call, price = fixture_call()
    call['usage'].pop('output_tokens')
    call['usage'].update(tokens=10, cost_micros=10)
    assert audit_call(call, price, 'synthetic-run') == (10, 10, True)


def test_live_forged_summary_rejected(tmp_path):
    root, journal = synthetic_journal(tmp_path, count=1)
    (journal / 'report.json').write_text(json.dumps({'experiment_id': 'forged', 'records': []}))
    with pytest.raises(ValueError, match='AGGREGATE_RECONCILIATION'):
        analyze(journal, root=root)
