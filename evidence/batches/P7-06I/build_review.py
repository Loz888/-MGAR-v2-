"""Build a review-only round3 candidate. This tool cannot run a model or Run."""
import copy
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import sys
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.path[:0] = [str(ROOT / 'scripts'), str(ROOT / 'tests')]
from source_fingerprint import fingerprint
from conftest import _admin_kwargs
import psycopg
from psycopg import sql

OUT = Path(__file__).with_name('round3-review')
NAMESPACE = 'mgar/P7-06/deepseek/flash-1017/2026-10-27/round3/'
SOURCES = ['internal/provider/deepseek.go', 'internal/demo/model_errors.go', 'internal/demo/worker.go',
           'agentkit/mgar_agents/runtime_worker.py', 'internal/demo/aggregate.go', 'internal/demo/tools.go',
           'internal/demo/run.go', 'internal/demo/dispatch.go', 'internal/policy/gateway.go',
           'internal/provider/input_bound.go', 'eval/live/pricing.json', 'eval/live/official-evidence.json']


def digest(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


def prior():
    result = []
    for name, report in [('P7-06-deepseek-flash-1017-20261026', 'actual-20261027'),
                         ('P7-06-round2-deepseek-flash-1017-20261027', 'round2-results-20261027')]:
        metrics_path = 'evidence/eval/live/' + report + '/metrics.json'
        report_path = 'evidence/eval/live/' + name + '/report.json'
        metrics = json.loads((ROOT / metrics_path).read_text())
        result.append({'experiment_id': name, 'report_path': report_path, 'report_sha256': digest(report_path),
                       'metrics_path': metrics_path, 'metrics_sha256': digest(metrics_path),
                       'known_cost_micros': metrics['known_cost_micros'], 'retained_upper_cost_micros': metrics['upper_cost_micros']})
    assert sum(row['retained_upper_cost_micros'] for row in result) == 311229
    return result


def validate(plan):
    assert plan['schema_version'] == 'live-experiment.proposal.v1' and plan['executable'] is False
    assert plan['review_status'] == 'READY_FOR_REVIEW' and plan['execution_authorized'] is False
    assert plan['provider'] == 'deepseek' and plan['model'] == 'flash-1017' and plan['output_tokens'] == 1024
    assert plan['approved_cap_micros'] == 1650000 and plan['prior'] == prior()
    assert plan['cumulative_candidate_cap_micros'] == 1961229 <= plan['project_cap_micros'] == 2000000
    assert plan['prior_upper_cost_micros'] == 311229
    assert plan['input_reservation_tokens'] == 48454 and plan['max_context_bytes'] == 48000
    quote = json.loads((ROOT / 'eval/live/pricing.json').read_text())
    price = quote['config']['price']
    bound = (48454 * price['input_micros_per_million'] + 1024 * price['output_micros_per_million'] + 999999) // 1000000
    assert plan['pricing'] == price and plan['pricing_reviewed_at'] == quote['reviewed_at']
    assert plan['single_max_call_bound_micros'] == bound == 15765 <= 17187
    assert plan['tokens_per_assignment'] == 65536 >= 48454 + 1024
    assert plan['max_context_tokens'] == 12000 and plan['temperature'] == 0
    assert plan['source_fingerprint'] == fingerprint()
    assert plan['source_sha256'] == {name: digest(name) for name in SOURCES}
    assert plan['manifest_sha256'] == digest('eval/cases/frozen-live-24.json')
    assert plan['dataset_manifest_sha256'] == digest('eval/dataset_manifest.json')
    expected = [{'case_id': f'live-{i:03d}', 'arm': arm,
                 'run_id': str(uuid.uuid5(uuid.NAMESPACE_URL, NAMESPACE + f'live-{i:03d}/{arm}')),
                 'cap_micros': 17187, 'status': 'NOT_RUN'} for i in range(1, 25) for arm in ['B0', 'B1', 'B2', 'B4']]
    assert plan['assignments'] == expected
    old_ids = set()
    for name in ['eval/live/plan.json', 'eval/live/round2/plan.json']:
        old_ids.update(row['run_id'] for row in json.loads((ROOT / name).read_text())['assignments'])
    assert not old_ids.intersection(row['run_id'] for row in expected)
    assert sum(row['cap_micros'] for row in expected) == 1649952 <= 1650000
    assert plan['automatic_retry'] is False and plan['unknown_stops_all'] is True
    assert plan['planner_scope'] == 'fixed_business_dag_template' and plan['not_run_arms'] == ['B3', 'A1', 'A2']


def main():
    plan = {'schema_version': 'live-experiment.proposal.v1', 'experiment_id': 'P7-06-round3-deepseek-flash-1017-20261027',
            'review_status': 'READY_FOR_REVIEW', 'executable': False, 'execution_authorized': False,
            'purpose': 'post_fix_regression_on_exposed_cases', 'provider': 'deepseek', 'model': 'flash-1017',
            'approved_cap_micros': 1650000, 'project_cap_micros': 2000000, 'prior_upper_cost_micros': 311229,
            'cumulative_candidate_cap_micros': 1961229, 'prior': prior(), 'temperature': 0,
            'output_tokens': 1024, 'input_reservation_tokens': 48454, 'max_context_bytes': 48000,
            'max_context_tokens': 12000, 'planner_scope': 'fixed_business_dag_template',
            'manifest_sha256': digest('eval/cases/frozen-live-24.json'), 'dataset_manifest_sha256': digest('eval/dataset_manifest.json'),
            'source_fingerprint': fingerprint(), 'source_sha256': {name: digest(name) for name in SOURCES},
            'not_run_arms': ['B3', 'A1', 'A2'], 'automatic_retry': False, 'unknown_stops_all': True,
            'allocation_run_id': str(uuid.uuid5(uuid.NAMESPACE_URL, NAMESPACE + 'allocation/all')),
            'assignments': [{'case_id': f'live-{i:03d}', 'arm': arm,
                 'run_id': str(uuid.uuid5(uuid.NAMESPACE_URL, NAMESPACE + f'live-{i:03d}/{arm}')),
                 'cap_micros': 17187, 'status': 'NOT_RUN'} for i in range(1, 25) for arm in ['B0', 'B1', 'B2', 'B4']],
            'remaining_gates': ['Review the protocol/cost/identity candidate.',
                'Implement a new separately reviewed round selector, cumulative checkpoint and independent analysis adapter.',
                'Run all PG/race/process/unknown-budget tests and re-freeze final executor source before activation.',
                'Check unchanged quote freshness, secret/network operation and unused marker/journal read-only.',
                'Only a separately authorized one-shot execution may send paid requests.'],
            'limitations': ['The same exposed24 cases are used. This is not an unseen holdout and rounds must not be pooled.',
                'Per-slot17187 is lower than round2 share19791. Cross-round gains cannot be attributed only to receipt changes.',
                'Development HTTP fixtures are local_stub, not model quality evidence.',
                'Data-verification and replan failures observed in round2 still require real evaluation.',
                'No old UNKNOWN reservation is released; unused shares are never transferred.',
                'A moving provider alias and worst-price estimates do not pin model weights or prove an invoice.']}
    quote = json.loads((ROOT / 'eval/live/pricing.json').read_text())
    plan.update(pricing=quote['config']['price'], pricing_reviewed_at=quote['reviewed_at'],
                single_max_call_bound_micros=15765, tokens_per_assignment=65536)
    validate(plan)
    checks = ['valid_candidate']
    for name in ['paid', 'budget', 'prior', 'identity', 'duplicate', 'source', 'model', 'output', 'manifest', 'price', 'bound', 'retry']:
        candidate = copy.deepcopy(plan)
        if name == 'paid': candidate['executable'] = True
        elif name == 'budget': candidate['approved_cap_micros'] = 2000000
        elif name == 'prior': candidate['prior'][0]['retained_upper_cost_micros'] = 0
        elif name == 'identity': candidate['assignments'][0]['run_id'] = json.loads((ROOT / 'eval/live/round2/plan.json').read_text())['assignments'][0]['run_id']
        elif name == 'duplicate': candidate['assignments'][1]['run_id'] = candidate['assignments'][0]['run_id']
        elif name == 'source': candidate['source_sha256'][SOURCES[0]] = '0' * 64
        elif name == 'model': candidate['model'] = 'other-model'
        elif name == 'output': candidate['output_tokens'] = 4096
        elif name == 'manifest': candidate['manifest_sha256'] = '0' * 64
        elif name == 'price': candidate['pricing']['output_micros_per_million'] = 0
        elif name == 'bound': candidate['single_max_call_bound_micros'] = 0
        else: candidate['automatic_retry'] = True
        try:
            validate(candidate)
        except AssertionError:
            checks.append('rejected_' + name)
        else:
            raise AssertionError('unsafe review variant admitted: ' + name)
    assert not (ROOT / 'evidence/eval/live' / plan['experiment_id']).exists()
    kwargs = _admin_kwargs()
    kwargs['options'] = '-c default_transaction_read_only=on -c statement_timeout=3000'
    with psycopg.connect(**kwargs) as conn:
        marker = conn.execute('SELECT count(*) FROM public.workflow_runs WHERE run_id=%s', (plan['allocation_run_id'],)).fetchone()[0]
        assert marker == 0
        models = 0
        ids = [row['run_id'] for row in plan['assignments']]
        namespaces = [row[0] for row in conn.execute("SELECT nspname FROM pg_namespace WHERE nspname='public' OR nspname~'^mgar_demo_[0-9a-f]{16}$'")]
        for namespace in namespaces:
            exists = conn.execute('SELECT to_regclass(%s)', (namespace + '.model_calls',)).fetchone()[0]
            if exists is not None:
                query = sql.SQL('SELECT count(*) FROM {} WHERE run_id::text=ANY(%s)').format(sql.Identifier(namespace, 'model_calls'))
                models += conn.execute(query, (ids,)).fetchone()[0]
        assert models == 0
    OUT.mkdir(exist_ok=True)
    (OUT / 'plan.json').write_text(json.dumps(plan, ensure_ascii=False, indent=2) + '\n')
    audit = {'timestamp_utc': datetime.now(timezone.utc).isoformat(), 'status': 'READY_FOR_REVIEW_NOT_EXECUTABLE',
             'plan_sha256': digest(str((OUT / 'plan.json').relative_to(ROOT))), 'checks': checks,
             'allocation_marker_rows': marker, 'model_calls': models, 'journal_exists': False,
             'single_max_call_bound_micros': 15765, 'slot_cap_micros': 17187,
             'new_cap_micros': 1650000, 'cumulative_candidate_cap_micros': 1961229,
             'actual_paid_calls': 0, 'runtime_adapter': 'NOT_IMPLEMENTED', 'execution': 'NOT_RUN'}
    (OUT / 'review-checks.json').write_text(json.dumps(audit, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(audit))


if __name__ == '__main__':
    main()
