"""Frozen inputs and independent mathematical truth; no Provider invocations."""
from pathlib import Path
import importlib.util
import sys
from collections import Counter

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'eval' / 'oracle'))


def test_frozen_dataset_partition_and_independent_oracle():
    spec = importlib.util.spec_from_file_location('validate_manifest', ROOT / 'eval/oracle/validate_manifest.py')
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    manifest = module.validate(ROOT)
    offline = [c for c in manifest['cases'] if c['split'] == 'offline']
    live = [c for c in manifest['cases'] if c['split'] == 'live']
    dev = [c for c in manifest['cases'] if c['split'] == 'development']
    assert len(offline) == 60 and set(Counter(c['category'] for c in offline).values()) == {6}
    assert len(live) == 24 and Counter(c['difficulty'] for c in live) == {'simple': 8, 'medium': 8, 'hard': 8}
    assert len(dev) >= 6
    assert {c['business'] for c in live} == {'incident', 'analysis'}


def test_independent_oracle_math_and_forged_claims():
    from build_dataset import build
    from reference import expected, judge
    cases, inputs, truths = build()
    for case in cases:
        data, truth = inputs[case['case_id']], truths[case['case_id']]
        assert judge(data, truth, truth)['semantic_pass']
        assert not judge(data, truth, truth | {'verified': True})['semantic_pass']
        assert not judge(data, truth, truth | {'input_version': 99})['semantic_pass']
        if truth['decision'] == 'answer':
            forged = truth | {'metrics': truth['metrics'] | {'paid_total': truth['metrics']['paid_total'] + 1}}
            assert not judge(data, truth, forged)['semantic_pass']
            false_bool = truth | {'metrics': truth['metrics'] | {'paid_orders': True}}
            assert not judge(data, truth, false_bool)['semantic_pass']
        assert not judge(data, truth, truth)['execution_verified']
    # Hand-calculated regression, independent of the fixture generator formula.
    data = inputs['offline-001'] | {'input_version': 1, 'prior_orders': [], 'orders': [
        {'event_id': 1, 'order_id': 101, 'status': 'paid', 'amount': 100},
        {'event_id': 2, 'order_id': 101, 'status': 'paid', 'amount': 100},
        {'event_id': 3, 'order_id': 102, 'status': 'paid', 'amount': 50},
        {'event_id': 4, 'order_id': 103, 'status': 'pending', 'amount': 70}]}
    assert expected(data)['metrics'] == {'paid_total': 150, 'paid_orders': 2, 'duplicate_paid_events': 1}


def test_frozen_truth_rejects_answer_changes_and_resealing(tmp_path):
    import copy
    import json
    import shutil
    import pytest
    from freeze import freeze
    from validate_manifest import validate
    baseline = validate(ROOT)
    for name in ['fixtures', 'eval']:
        shutil.copytree(ROOT / name, tmp_path / name)
    lock_before = (tmp_path / f"eval/freezes/{baseline['dataset_version']}.sha256").read_bytes()
    truth_path = tmp_path / baseline['truth_path']
    truth = json.loads(truth_path.read_bytes())
    changed = copy.deepcopy(truth)
    changed['offline-001']['metrics']['paid_total'] += 1
    truth_path.write_text(json.dumps(changed))
    with pytest.raises(ValueError, match='VERSION_REQUIRED'):
        validate(tmp_path)
    with pytest.raises(ValueError, match='INCREMENT_VERSION'):
        freeze(tmp_path, baseline['dataset_version'])
    assert (tmp_path / f"eval/freezes/{baseline['dataset_version']}.sha256").read_bytes() == lock_before


def test_seeded_databases_are_reproducible_isolated_and_readonly():
    import json
    import psycopg
    from psycopg import sql
    import pytest
    from seed import seeded
    from validate_manifest import validate
    manifest = validate(ROOT)
    inputs = json.loads((ROOT / manifest['inputs_path']).read_bytes())
    # All 60 engineering cases get an actual fresh schema and reader identity.
    for case in [c for c in manifest['cases'] if c['split'] == 'offline']:
        data = inputs[case['case_id']]
        with seeded(data) as database:
            expected_rows = [(r['event_id'], r['order_id'], r['status'], r['amount']) for r in data['orders']]
            assert database.rows() == expected_rows
            with database.connect_reader() as reader:
                assert reader.execute('SHOW transaction_read_only').fetchone()[0] == 'on'
                with pytest.raises(psycopg.errors.ReadOnlySqlTransaction):
                    reader.execute(sql.SQL('DELETE FROM {}.orders').format(sql.Identifier(database.schema)))
    with seeded(inputs['offline-001']) as first, seeded(inputs['offline-001']) as second:
        assert first.schema != second.schema and first.role != second.role
        assert first.rows() == second.rows()
        with first.connect_reader() as reader:
            with pytest.raises(psycopg.errors.InsufficientPrivilege):
                reader.execute(sql.SQL('SELECT * FROM {}.orders').format(sql.Identifier(second.schema)))
