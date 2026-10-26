"""Read-only evaluation of the approved live journal; never sends model requests."""
import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import re
import sys
import uuid

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from eval.analysis.metrics import number, paired_delta, percentile, rate  # noqa: E402
from eval.analysis.report import csv_file  # noqa: E402
from eval.oracle.reference import judge  # noqa: E402
from eval.runner import KEYS, TREATMENTS, canonical  # noqa: E402
from eval.live.tokenizer_data import proof_fingerprint  # noqa: E402

EXPERIMENT = 'P7-06-deepseek-flash-1017-20261026'
MANIFEST_SHA = 'ec6d5576a75fc2724756abeef2628189c2922038f09cb9258d7a1972e493c8ab'
DATASET_SHA = '0b48a86008574d0a8bdbde1d88d37e718d45e75fe33363075fdcd48f0eb2d511'
ARMS = ['B0', 'B1', 'B2', 'B4']
CAP = 20833


def require(ok, reason):
    if not ok:
        raise ValueError(reason)


def integer(value):
    require(type(value) is int and 0 <= value <= 10**12, 'INVALID_INTEGER')
    return value


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def hash_string(value):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{64}', value) is not None


def safe_path(path):
    path = Path(path).absolute()
    require('..' not in path.parts and not any(p.is_symlink() for p in [path, *path.parents]), 'UNSAFE_EVIDENCE_PATH')
    return path


def read_bytes(path):
    path = safe_path(path)
    require(path.is_file() and path.stat().st_size <= 32 * 1024 * 1024, 'INVALID_EVIDENCE_FILE')
    return path.read_bytes()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'DUPLICATE_JSON_KEY')
        result[key] = value
    return result


def read_json(path):
    return json.loads(read_bytes(path), object_pairs_hook=unique_object,
                      parse_constant=lambda _: require(False, 'NONFINITE_JSON'))


def frozen(root):
    plan = read_json(root / 'eval/live/plan.json')
    expected = [{'case_id': f'live-{i:03d}', 'arm': arm,
                 'run_id': str(uuid.uuid5(uuid.NAMESPACE_URL, f'mgar/P7-06/deepseek/flash-1017/2026-10-26/live-{i:03d}/{arm}')),
                 'cap_micros': CAP, 'status': 'NOT_RUN'} for i in range(1, 25) for arm in ARMS]
    require(plan == {'schema_version': 'live-experiment.v1', 'experiment_id': EXPERIMENT,
                     'provider': 'deepseek', 'model': 'flash-1017', 'approved_cap_micros': 2000000,
                     'manifest_sha256': MANIFEST_SHA, 'dataset_manifest_sha256': DATASET_SHA,
                     'temperature': 0, 'max_context_bytes': 48000, 'max_context_tokens': 12000,
                     'planner_scope': 'fixed_business_dag_template', 'arms': ARMS,
                     'not_run_arms': ['B3', 'A1', 'A2'], 'assignments': expected}, 'FROZEN_ASSIGNMENT_CHANGED')
    for name, sha in [('eval/cases/frozen-live-24.json', MANIFEST_SHA), ('eval/dataset_manifest.json', DATASET_SHA)]:
        require(digest(read_bytes(root / name)) == sha, 'FROZEN_MANIFEST_CHANGED')
    manifest = read_json(root / 'eval/dataset_manifest.json')
    for name, sha in manifest['files'].items():
        path = safe_path(root / name)
        require(path.is_relative_to(root) and digest(read_bytes(path)) == sha, 'FROZEN_DATA_CHANGED')
    cases = {c['case_id']: c for c in manifest['cases'] if c['split'] == 'live'}
    return plan, cases, read_json(root / manifest['inputs_path']), read_json(root / manifest['truth_path'])


def audit_call(call, price, run_id, *, bound=None):
    """Return known cost (or None), conservative upper cost, and usage coverage."""
    require(price.get('provider') == 'deepseek' and price.get('model') == 'flash-1017'
            and isinstance(price.get('version'), str) and price['version'], 'INVALID_PRICE')
    for key in ['input_micros_per_million', 'output_micros_per_million']:
        require(0 < integer(price.get(key)) <= 10**9, 'INVALID_PRICE')
    require(call.get('run_id') == run_id and call.get('provider') == 'deepseek'
            and call.get('model') == 'flash-1017' and isinstance(call.get('call_id'), str)
            and call['call_id'], 'CALL_IDENTITY')
    reservation, usage = call.get('reservation') or {}, call.get('usage') or {}
    require(reservation.get('pricing') == price and reservation.get('pricing_version') == price['version']
            and hash_string(reservation.get('input_digest')), 'CALL_RESERVATION')

    def cost(source):
        # Go's durable JSON omits zero input/output fields; totals and cost remain explicit.
        inp, out = integer(source.get('input_tokens', 0)), integer(source.get('output_tokens', 0))
        require(inp + out == integer(source.get('tokens')) and inp + out <= 10000000, 'CALL_TOKENS')
        computed = (inp * price['input_micros_per_million'] + out * price['output_micros_per_million'] + 999999) // 1000000
        require(integer(source.get('cost_micros')) == computed, 'CALL_COST')
        return inp, out, computed

    rin, rout, reserved = cost(reservation)
    require(rin + rout > 0, 'EMPTY_RESERVATION')
    if bound is None:
        require('.u8-' not in price['version'], 'MISSING_INPUT_BOUND')
    else:
        fingerprint = proof_fingerprint(bound.get('proof'))
        maximum = integer(bound.get('maximum_input_tokens'))
        require(price['version'].endswith('.u8-' + fingerprint[:16])
                and 0 < rin <= maximum <= 65536
                and rout == integer(bound.get('output_tokens')), 'INPUT_BOUND_MISMATCH')
    if call.get('usage_known') is False:
        require(call.get('status') in ['reserved', 'unknown'] and not usage
                and call.get('usage_source') in ['unknown', 'conservative_bound'], 'UNKNOWN_CALL_CHANGED')
        return None, reserved, False
    require(call.get('usage_known') is True and call.get('usage_source') == 'provider'
            and call.get('status') in ['succeeded', 'failed'], 'NON_PROVIDER_USAGE')
    inp, out, spent = cost(usage)
    require(inp <= rin and out <= rout and spent <= reserved
            and usage.get('pricing_version') == price['version']
            and isinstance(usage.get('response_model'), str) and usage['response_model']
            and hash_string(usage.get('response_sha256')), 'CALL_USAGE_INVALID')
    # Accept the server's reported identifier, retaining it even when it is an alias.
    # The requested identifier is always the frozen flash-1017; no model substitution.
    for key, expected in [('status', call['status']), ('known', True), ('source', 'provider')]:
        if key in usage:
            require(usage[key] == expected, 'CALL_USAGE_MISMATCH')
    if call.get('settlement') is not None:
        require(call['settlement'] == usage, 'CALL_SETTLEMENT_MISMATCH')
    return spent, spent, True


def launch_price(path, plan, root):
    preflight = read_json(path / 'preflight.json')
    require(preflight.get('ready') is True and preflight.get('blockers') == []
            and preflight.get('assignments') == plan['assignments']
            and preflight.get('cap_micros') == 2000000 and preflight.get('allocated_micros') == 96 * CAP,
            'INVALID_LAUNCH_PREFLIGHT')
    evidence = preflight.get('pricing') or {}
    cfg = evidence.get('config') or {}
    require(evidence.get('status') == 'VERIFIED'
            and evidence.get('pricing_url') == 'https://api-docs.deepseek.com/quick_start/pricing'
            and evidence.get('models_url') == 'https://api.deepseek.com/models'
            and hash_string(cfg.get('official_evidence_sha256')), 'UNVERIFIED_PRICE')
    official = read_bytes(root / 'eval/live/official-evidence.json')
    require(digest(official) == cfg['official_evidence_sha256'],
            'OFFICIAL_EVIDENCE_HASH')
    executor = read_json(path / 'executor.json')
    require(hash_string(executor.get('sha256')), 'EXECUTOR_IDENTITY')
    bound = None
    if cfg.get('input_bound') is None:
        require('.u8-' not in cfg['price']['version'], 'MISSING_INPUT_BOUND')
    if cfg.get('input_bound') is not None:
        bound = {'proof': cfg['input_bound'], 'maximum_input_tokens': preflight.get('input_reservation_tokens'),
                 'output_tokens': cfg.get('output_tokens')}
        described = json.loads(official, object_pairs_hook=unique_object).get('input_bound') or {}
        require(cfg.get('max_prompt_bytes') == 48000 and cfg.get('output_tokens') == 1024
                and described.get('proof_sha256') == proof_fingerprint(cfg['input_bound'])
                and described.get('maximum_request_input_tokens') == bound['maximum_input_tokens'], 'INPUT_BOUND_EVIDENCE')
    return cfg['price'], bound


def execution_pass(raw, case):
    oracle = raw.get('execution_oracle') or {}
    required = case['business'] == 'incident' and not case['negative']
    require(oracle.get('version') == 'experiment-execution-oracle.v1'
            and oracle.get('required') is required, 'EXECUTION_ORACLE_CONTRACT')
    if not required:
        return oracle.get('passed') is True
    observation = oracle.get('observation') or {}
    return (oracle.get('passed') is True and oracle.get('sandbox_executions') == 1
            and hash_string(oracle.get('source_sha256')) and type(observation.get('exit_code')) is int
            and observation['exit_code'] == 0 and type(observation.get('tests')) is int and observation['tests'] > 0)


def audit_raw(raw, assignment, price, case, seen, bound=None):
    require(raw.get('run_id') == assignment['run_id'] and raw.get('arm') == assignment['arm']
            and (raw.get('case') or {}).get('case_id') == case['case_id'], 'RAW_IDENTITY')
    expected_case = {k: case[k] for k in ['case_id', 'category', 'seed', 'negative', 'limits']}
    expected_case['limits'] = case['limits'] | {'cost_micros': CAP}
    require(raw.get('schema_version') == 'experiment.v1' and canonical(raw.get('case')) == canonical(expected_case)
            and canonical(raw.get('treatment')) == canonical(dict(zip(KEYS, TREATMENTS[assignment['arm']], strict=True))),
            'RAW_EXPERIMENT_CONTRACT')
    models = raw.get('model_calls') or []
    known, upper, settled, reserved, settled_tokens, reserved_tokens = 0, 0, 0, 0, 0, 0
    for call in models:
        cost, call_upper, complete = audit_call(call, price, assignment['run_id'], bound=bound)
        require(call['call_id'] not in seen, 'DUPLICATE_CALL')
        seen.add(call['call_id'])
        known += cost or 0
        upper += call_upper
        settled += complete
        reserved += not complete
        if complete:
            settled_tokens += call['usage']['tokens']
        else:
            reserved_tokens += call['reservation']['tokens']
    budget = (raw.get('result') or {}).get('budget') or {}
    require(budget.get('run_id') == assignment['run_id'] and budget.get('conserved') is True
            and (budget.get('limits') or {}).get('cost_micros') == CAP and upper <= CAP, 'RAW_BUDGET')
    limits = expected_case['limits']
    expected_limits = {k: limits[k] for k in ['model_calls', 'tool_calls', 'tokens', 'cost_micros']}
    expected_limits.update(concurrent_calls=4, task_concurrency=4, delegations=32)
    require(canonical(budget['limits']) == canonical(expected_limits)
            and settled + reserved <= limits['model_calls'] and settled_tokens + reserved_tokens <= limits['tokens'],
            'RAW_BUDGET_LIMITS')
    for field, value in [('settled_cost_micros', known), ('reserved_cost_micros', upper - known),
                         ('settled_models', settled), ('reserved_models', reserved),
                         ('settled_tokens', settled_tokens), ('reserved_tokens', reserved_tokens)]:
        require(integer(budget.get(field)) == value, 'LEDGER_RECONCILIATION')
    require(integer(budget.get('unknown_models')) + integer(budget.get('pending_models')) == reserved,
            'UNKNOWN_LEDGER_RECONCILIATION')
    require(len(raw.get('tool_calls') or []) == integer(budget.get('settled_tools')) + integer(budget.get('reserved_tools')),
            'TOOL_LEDGER_RECONCILIATION')
    require(integer(budget.get('pending_tools')) + integer(budget.get('unknown_tools')) == integer(budget.get('reserved_tools')),
            'TOOL_LEDGER_RECONCILIATION')
    tool_states = Counter()
    for tool in raw.get('tool_calls') or []:
        require(tool.get('run_id') == assignment['run_id'] and tool.get('tool_version') == 'demo.v1'
                and isinstance(tool.get('invocation_id'), str) and tool['invocation_id'] not in seen, 'TOOL_IDENTITY')
        seen.add(tool['invocation_id'])
        require(tool.get('status') in ['reserved', 'unknown', 'succeeded', 'failed'], 'TOOL_LEDGER_RECONCILIATION')
        tool_states[tool['status']] += 1
    require(tool_states['reserved'] == budget['pending_tools'] and tool_states['unknown'] == budget['unknown_tools']
            and tool_states['succeeded'] + tool_states['failed'] == budget['settled_tools'], 'TOOL_LEDGER_RECONCILIATION')
    if assignment['arm'] != 'B4':
        require(raw.get('workflow_success') is None and raw['result'].get('verdict_count') == 0,
                'BASELINE_VERIFICATION_ELEVATED')
    return known, upper, settled, reserved


def verified_kinds(raw):
    kinds, ids = set(), set()
    for artifact in raw['result'].get('artifacts') or []:
        meta, verdict = artifact.get('metadata') or {}, artifact.get('verdict') or {}
        body = artifact.get('body')
        require(isinstance(body, str) and digest(body.encode()) == meta.get('content_sha256')
                and len(body.encode()) == integer(meta.get('byte_length'))
                and meta.get('producer_run_id') == raw['run_id']
                and isinstance(meta.get('artifact_id'), str) and meta['artifact_id'] not in ids,
                'ARTIFACT_PROVENANCE')
        ids.add(meta['artifact_id'])
        if (verdict.get('status') == 'passed' and verdict.get('schema_version') == 'verdict.v1'
                and verdict.get('verifier_version') == 'verifier.v1'
                and verdict.get('artifact_id') == meta['artifact_id'] and verdict.get('checks')
                and all(c.get('status') == 'passed' for c in verdict['checks'])):
            kinds.add(meta.get('kind'))
    return kinds


def summary(rows):
    evaluated = [r for r in rows if r['oracle_pass'] is not None]
    passed = sum(r['oracle_pass'] is True for r in rows)
    return {'assigned': len(rows), 'statuses': dict(Counter(r['status'] for r in rows)),
            'evaluated': len(evaluated), 'oracle_pass': passed,
            'oracle_pass_rate_observed': rate(passed, len(evaluated)),
            'oracle_pass_rate_all_assigned': rate(passed, len(rows)) if evaluated else None,
            'known_cost_micros': sum(r['known_cost_micros'] for r in rows),
            'upper_cost_micros': sum(r['upper_cost_micros'] for r in rows),
            'latency_p50_ms': percentile([r['latency_ms'] for r in rows if r['latency_ms'] is not None], 50),
            'latency_p95_ms': percentile([r['latency_ms'] for r in rows if r['latency_ms'] is not None], 95)}


def analyze(input_dir, *, root=ROOT):
    root, path = safe_path(root), safe_path(input_dir)
    plan, cases, inputs, truths = frozen(root)
    exists = path.exists()
    if exists:
        require(read_json(path / 'assignment.json') == plan, 'ASSIGNMENT_CHANGED')
        expected_files = {'assignment.json', 'preflight.json', 'executor.json', 'report.json'}
        expected_files |= {f'{i:03d}.{kind}.json' for i in range(96) for kind in ['started', 'result', 'raw']}
        require({p.name for p in path.iterdir()} <= expected_files, 'UNEXPECTED_JOURNAL_FILE')
    rows, seen, price, input_bound = [], set(), None, None
    stopped = False
    for i, assignment in enumerate(plan['assignments']):
        row = dict(assignment, category=cases[assignment['case_id']]['category'], oracle_pass=None,
                   real_model_closure_verified=False, known_cost_micros=0, upper_cost_micros=0,
                   model_calls=0, known_model_calls=0, estimated_cost_usd=None, latency_ms=None,
                   response_models=[], reason='not_started')
        rows.append(row)
        started, result, raw_path = [path / f'{i:03d}.{kind}.json' for kind in ['started', 'result', 'raw']]
        if not exists or not started.exists():
            require(not result.exists() and not raw_path.exists(), 'MISSING_START_RECORD')
            stopped = True
            continue
        require(not stopped, 'NONSEQUENTIAL_JOURNAL')
        require(read_json(started) == assignment, 'START_ASSIGNMENT_CHANGED')
        row.update(status='UNKNOWN', upper_cost_micros=CAP, reason='incomplete_execution_or_accounting')
        if not result.exists():
            stopped = True
            continue
        record = read_json(result)
        require(record.get('assignment') == assignment, 'RESULT_ASSIGNMENT_CHANGED')
        require(raw_path.exists() and digest(read_bytes(raw_path)) == record.get('raw_sha256'), 'RAW_HASH_CHANGED')
        raw = read_json(raw_path)
        status = record.get('status')
        require(status in ['completed', 'failed', 'blocked', 'UNKNOWN'], 'INVALID_RESULT_STATUS')
        # A failed fixture startup can yield a partial report. It never proves a
        # complete ledger, even if the partial JSON happens to contain zero calls.
        if status == 'UNKNOWN' and not (raw.get('result') or {}).get('budget'):
            require(integer(record.get('known_cost_micros')) == 0
                    and integer(record.get('upper_cost_micros')) == CAP, 'UNKNOWN_COST_BOUND')
            stopped = True
            continue
        if price is None:
            price, input_bound = launch_price(path, plan, root)
        known, upper, settled, reserved = audit_raw(raw, assignment, price, cases[assignment['case_id']], seen, input_bound)
        require(integer(record.get('known_cost_micros')) == known, 'RESULT_COST_CHANGED')
        if status == 'UNKNOWN':
            require(integer(record.get('upper_cost_micros')) == CAP, 'UNKNOWN_COST_BOUND')
            stopped = True
        else:
            require(integer(raw['result']['budget'].get('reserved_tools')) == 0, 'UNFINISHED_TOOL_ACCOUNTING')
            require(reserved == 0 and raw.get('execution_status') == status
                    and integer(record.get('upper_cost_micros')) == upper, 'RESULT_ACCOUNTING_CHANGED')
        row.update(status=status, known_cost_micros=known, upper_cost_micros=CAP if status == 'UNKNOWN' else upper,
                   model_calls=settled + reserved, known_model_calls=settled,
                   response_models=sorted({c['usage']['response_model'] for c in raw.get('model_calls') or [] if c['usage_known']}))
        if status == 'UNKNOWN':
            continue
        row.update(estimated_cost_usd=known / 1000000, reason='', latency_ms=number(raw['experiment_elapsed_ms']))
        case_id = assignment['case_id']
        semantic = judge(inputs[case_id], truths[case_id], raw.get('answer'))
        row['oracle_pass'] = semantic['semantic_pass'] and execution_pass(raw, cases[case_id])
        # Require the controller's strict terminal state and actual verifier artifacts.
        kinds = verified_kinds(raw)
        required_kinds = {'finding', 'sql_evidence', 'report'} | ({'patch'} if cases[case_id]['business'] == 'incident' else set())
        row['real_model_closure_verified'] = bool(row['oracle_pass'] and assignment['arm'] == 'B4'
            and raw.get('workflow_success') is True and raw['result'].get('state') == 'succeeded'
            and required_kinds <= kinds and settled > 0)
    comparisons = []
    for left, right in [('B0', 'B1'), ('B1', 'B2'), ('B2', 'B4'), ('B0', 'B4')]:
        for metric in ['oracle_pass', 'estimated_cost_usd', 'latency_ms']:
            groups = {arm: {r['case_id']: int(r[metric]) if metric == 'oracle_pass' and r[metric] is not None else r[metric]
                            for r in rows if r['arm'] == arm} for arm in [left, right]}
            missing = sum(any(groups[a][c] is None for a in groups) for c in groups[left])
            comparison = ({'pairs': 0, 'missing_pairs': missing, 'delta': None, 'ci95': None,
                           'reason': 'all_24_pairs_required_no_pair_dropping'} if missing else paired_delta(groups[left], groups[right]))
            comparisons.append(dict(left=left, right=right, metric=metric, **comparison))
    complete = all(r['status'] in ['completed', 'failed', 'blocked'] for r in rows)
    closure = any(r['real_model_closure_verified'] for r in rows)
    known = sum(r['known_cost_micros'] for r in rows)
    upper = sum(r['upper_cost_micros'] for r in rows)
    require(known <= upper <= 96 * CAP, 'GLOBAL_COST_BOUND')
    if (path / 'report.json').exists():
        aggregate = read_json(path / 'report.json')
        require(aggregate.get('experiment_id') == EXPERIMENT
                and integer(aggregate.get('known_cost_micros')) == known
                and integer(aggregate.get('upper_cost_micros')) == upper
                and len(aggregate.get('records', [])) == 96, 'AGGREGATE_RECONCILIATION')
        for record, row, assignment in zip(aggregate['records'], rows, plan['assignments'], strict=True):
            require(record.get('assignment') == assignment and all(record.get(k) == row[k] for k in
                    ['status', 'known_cost_micros', 'upper_cost_micros']), 'AGGREGATE_RECORD_CHANGED')
    return {'schema_version': 'live-analysis.v1', 'experiment_id': EXPERIMENT,
            'status': 'EVALUATED' if complete and closure else 'BLOCKED',
            'assigned': 96, 'real_model_closure_verified': closure, 'all_assignments_evaluated': complete,
            'known_cost_micros': known, 'upper_cost_micros': upper,
            'estimated_cost_usd': known / 1000000 if complete else None,
            'recorded_model_calls': sum(r['model_calls'] for r in rows),
            'known_model_calls': sum(r['known_model_calls'] for r in rows),
            'not_run_arms': ['B3', 'A1', 'A2'], 'per_case': rows, 'paired': comparisons,
            'arms': {arm: summary([r for r in rows if r['arm'] == arm]) for arm in ARMS},
            'categories': [dict(arm=arm, category=cat, **summary([r for r in rows if r['arm'] == arm and r['category'] == cat]))
                           for arm in ARMS for cat in sorted({r['category'] for r in rows})],
            'input_hashes': {p.name: digest(read_bytes(p)) for p in sorted(path.iterdir())} if exists else {},
            'limitations': ['No journal is NOT_RUN, never a successful zero-cost experiment.',
                            'Known provider estimates and unknown upper bounds are not a supplier invoice.',
                            'Hash integrity assumes trusted controller journals; it is not provider attestation.',
                            'Independent mathematics is rerun here; SQL/sandbox/Verifier evidence is captured by the trusted executor.',
                            'Crash with incomplete ledger retains the entire share; reconcile retained PG before any recovery.',
                            'One sequential run per frozen case; bootstrap conditions on cases, not model sampling uncertainty.',
                            'Planner uses the frozen business DAG template; no unrestricted planning claim.',
                            'B3/A1/A2 were not run; no causal claim about their effects.']}


def generate(input_dir, output):
    output = safe_path(output)
    require(any(output.is_relative_to(ROOT / part) for part in ['evidence/eval/live', '.local']), 'UNSAFE_OUTPUT_PATH')
    require(not output.exists() and not output.is_relative_to(safe_path(input_dir)), 'OUTPUT_ALREADY_EXISTS_OR_OVERLAPS_INPUT')
    result = analyze(input_dir)
    output.mkdir(parents=True, exist_ok=False)
    (output / 'metrics.json').write_text(json.dumps(result, indent=2, ensure_ascii=False, allow_nan=False) + '\n')
    for name, rows in [('per_case', result['per_case']), ('by_arm', [dict(arm=a, **s) for a, s in result['arms'].items()]),
                       ('by_category', result['categories']), ('paired', result['paired'])]:
        csv_file(output / (name + '.csv'), rows)
    lines = ['# P7-06C live evaluation', '', f"Status: {result['status']}; assigned: 96; real model closure verified: {result['real_model_closure_verified']}.", '',
             '| Arm | Assigned | Evaluated | Oracle pass | NOT_RUN | UNKNOWN |', '|---|---:|---:|---:|---:|---:|']
    for arm, s in result['arms'].items():
        lines.append(f"| {arm} | {s['assigned']} | {s['evaluated']} | {s['oracle_pass']} | {s['statuses'].get('NOT_RUN', 0)} | {s['statuses'].get('UNKNOWN', 0)} |")
    lines += ['', 'No conclusion about improvement is implied by this report.', '', *['- ' + x for x in result['limitations']], '']
    (output / 'report.md').write_text('\n'.join(lines))
    (output / 'files.sha256.json').write_text(json.dumps({p.name: digest(p.read_bytes()) for p in sorted(output.iterdir())}, indent=2) + '\n')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, default=ROOT / 'evidence/eval/live' / EXPERIMENT)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    try:
        report = generate(args.input, args.output)
    except (ValueError, OSError, KeyError, TypeError) as exc:
        print(json.dumps({'status': 'INVALID_EVIDENCE', 'error_type': type(exc).__name__}))
        return 1
    print(json.dumps({k: report[k] for k in ['status', 'assigned', 'recorded_model_calls', 'real_model_closure_verified']}))
    return 0 if report['status'] == 'EVALUATED' else 2


if __name__ == '__main__':
    sys.exit(main())
