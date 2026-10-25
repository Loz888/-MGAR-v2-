"""Owned offline experiments; immutable assignment and append-only raw evidence."""
from __future__ import annotations

import argparse
import csv
from datetime import datetime, timezone
import gzip
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from eval.oracle.reference import judge  # noqa: E402

MANIFEST = '0b48a86008574d0a8bdbde1d88d37e718d45e75fe33363075fdcd48f0eb2d511'
TREATMENTS = {
    'B0': (False, False, 'full', False, False),
    'B1': (True, False, 'full', False, False),
    'B2': (True, True, 'full', False, True),
    'B3': (True, True, 'selected', False, True),
    'B4': (True, True, 'selected', True, True),
    'A1': (True, True, 'selected', True, False),
    'A2': (True, True, 'selected', False, True),
}
KEYS = ('multi_role', 'dynamic_plan', 'context', 'internal_verifier', 'replan')
SHARED = {
    'provider': 'mock', 'model': 'demo-mock.v1', 'temperature': 0,
    'tool_version': 'demo.v1', 'pricing_version': 'demo-free.v1',
    'max_context_bytes': 48000, 'max_context_tokens': 12000,
    'limits_source': 'frozen_case', 'max_delegations': 32,
    'core_tools': ['research.read', 'data.query', 'action.patch', 'action.test'],
    'oracle': 'dataset-oracle.v1+experiment-execution-oracle.v1', 'manifest_sha256': MANIFEST,
}


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':'), allow_nan=False).encode()


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def validate_config(config):
    expected = {'schema_version': 'experiment-arms.v1', 'shared': SHARED,
                'arms': {arm: dict(zip(KEYS, values, strict=True)) for arm, values in TREATMENTS.items()}}
    if canonical(config) != canonical(expected):
        raise ValueError('UNFAIR_OR_UNKNOWN_CONFIGURATION')


def load_config(path):
    # JSON is a YAML subset, avoiding another parser dependency for frozen config.
    config = json.loads(Path(path).read_text())
    validate_config(config)
    return config


def append_record(path, record):
    with Path(path).open('ab') as stream:
        stream.write(canonical(record) + b'\n')
        stream.flush()
        os.fsync(stream.fileno())


def validate_complete(rows, cases, arms):
    expected = {(case, arm) for case in cases for arm in arms}
    actual = [(row['case_id'], row['arm']) for row in rows]
    if len(actual) != len(expected) or set(actual) != expected:
        raise ValueError('MISSING_OR_DUPLICATE_ASSIGNMENT')


def save_new(path, value):
    with path.open('xb') as stream:
        stream.write(canonical(value) + b'\n')
        stream.flush()
        os.fsync(stream.fileno())


def archive(path, raw):
    with path.open('xb') as stream:
        with gzip.GzipFile(fileobj=stream, mode='wb', mtime=0) as zipped:
            zipped.write(raw)
        stream.flush()
        os.fsync(stream.fileno())
    return digest(path.read_bytes())


def validate_observed(report, case, arm, config):
    """Configuration labels alone do not establish observed fairness."""
    if report.get('treatment') != config['arms'][arm] or report.get('case', {}).get('seed') != case['seed']:
        raise ValueError('OBSERVED_TREATMENT_MISMATCH')
    budget = report['result']['budget']
    expected = case['limits'] | {'concurrent_calls': case['limits']['max_parallel'],
                               'task_concurrency': case['limits']['max_parallel'], 'delegations': 32}
    del expected['max_parallel']
    if budget['limits'] != expected or not budget['conserved']:
        raise ValueError('OBSERVED_BUDGET_MISMATCH')
    if len(report['model_calls']) != budget['settled_models'] + budget['reserved_models']:
        raise ValueError('MODEL_LEDGER_MISSING')
    if len(report['tool_calls']) != budget['settled_tools'] + budget['reserved_tools']:
        raise ValueError('TOOL_LEDGER_MISSING')
    for call in report['model_calls']:
        if call['provider'] != SHARED['provider'] or call['model'] != SHARED['model']:
            raise ValueError('OBSERVED_MODEL_MISMATCH')
    for call in report['tool_calls']:
        if call['tool_version'] != SHARED['tool_version']:
            raise ValueError('OBSERVED_TOOL_MISMATCH')
    if not config['arms'][arm]['internal_verifier']:
        if report['workflow_success'] is not None or report['result']['verdict_count'] != 0:
            raise ValueError('BASELINE_VERIFICATION_ELEVATED')


def summarize(rows, arms):
    summaries = {}
    for arm in arms:
        assigned = [row for row in rows if row['arm'] == arm]
        passed = sum(row['oracle_pass'] for row in assigned)
        summaries[arm] = {'assigned': len(assigned), 'oracle_pass': passed,
                          'oracle_fail': len(assigned) - passed,
                          'task_success_rate': passed / len(assigned),
                          'execution_failures': sum(row['execution_status'] == 'failed' for row in assigned),
                          'infrastructure_failures': sum(bool(row['infrastructure_error']) for row in assigned),
                          'model_calls': sum(row['model_calls'] for row in assigned),
                          'tool_calls': sum(row['tool_calls'] for row in assigned)}
    return {'schema_version': 'experiment-summary.v1', 'evidence_level': 'E0_MOCK_ENGINEERING',
            'assigned': len(rows), 'arms': summaries, 'paid_calls': 0}


def verify_output(output):
    """Read back durable files, matching assignment, raw reports and ledgers."""
    assignment = json.loads((output / 'assignment.json').read_text())
    validate_config(assignment['config'])
    if assignment['config_sha256'] != digest(canonical(assignment['config'])) or assignment['manifest_sha256'] != MANIFEST:
        raise ValueError('ASSIGNMENT_CONFIGURATION_CHANGED')
    inputs = json.loads((ROOT / 'fixtures/datasets/v1/inputs.json').read_text())
    truths = json.loads((ROOT / 'eval/oracle/v1_truth.json').read_text())
    records = {name: [json.loads(line) for line in (output / (name + '.jsonl')).read_text().splitlines()]
               for name in ['runs', 'model_calls', 'tool_calls', 'oracle_judgments', 'timings']}
    case_ids = [case['case_id'] for case in assignment['cases']]
    for name in ['runs', 'oracle_judgments', 'timings']:
        validate_complete(records[name], case_ids, assignment['arms'])
    for name, key in [('model_calls', 'call_id'), ('tool_calls', 'invocation_id')]:
        ids = [row[key] for row in records[name]]
        if len(ids) != len(set(ids)):
            raise ValueError('DUPLICATE_LEDGER_ID')
    wanted_models, wanted_tools = [], []
    judgments = {(row['case_id'], row['arm']): row for row in records['oracle_judgments']}
    cases = {row['case_id']: row for row in assignment['cases']}
    for row in records['runs']:
        stem = row['case_id'] + '-' + row['arm']
        if row['raw'] != 'raw/' + stem + '.json.gz':
            raise ValueError('RAW_PATH_MISMATCH')
        raw = (output / row['raw']).read_bytes()
        trace = (output / 'traces' / (stem + '.json.gz')).read_bytes()
        if digest(raw) != row['raw_sha256'] or digest(trace) != row['trace_sha256']:
            raise ValueError('RAW_EVIDENCE_CHANGED')
        if row['infrastructure_error']:
            continue
        report = json.loads(gzip.decompress(raw))
        if report['run_id'] != row['run_id'] or report['arm'] != row['arm'] or report['case']['case_id'] != row['case_id']:
            raise ValueError('RAW_IDENTITY_MISMATCH')
        validate_observed(report, cases[row['case_id']], row['arm'], assignment['config'])
        judgment = judge(inputs[row['case_id']], truths[row['case_id']], report['answer'])
        oracle_pass = judgment['semantic_pass'] and report['execution_oracle']['passed'] is True
        recorded = judgments[(row['case_id'], row['arm'])]
        if (row['oracle_pass'] != oracle_pass or recorded['oracle_pass'] != oracle_pass
                or row['answer'] != report['answer'] or recorded['run_id'] != report['run_id']):
            raise ValueError('ORACLE_RECORD_MISMATCH')
        wanted_models += [(row['arm'], row['case_id'], call['call_id']) for call in report['model_calls']]
        wanted_tools += [(row['arm'], row['case_id'], call['invocation_id']) for call in report['tool_calls']]
    # Even technical failures keep any recovered ledger rows. For a fully
    # captured run, exact equality proves neither successful nor failed calls vanished.
    if not any(row['infrastructure_error'] for row in records['runs']):
        for wanted, name, key in [(wanted_models, 'model_calls', 'call_id'), (wanted_tools, 'tool_calls', 'invocation_id')]:
            actual = [(row['arm'], row['case_id'], row[key]) for row in records[name]]
            if sorted(wanted) != sorted(actual):
                raise ValueError('RAW_LEDGER_MISMATCH')
    return {'assigned': len(records['runs']), 'model_records': len(records['model_calls']),
            'tool_records': len(records['tool_calls']), 'raw_hashes_valid': True}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--provider', choices=['mock'], default='mock')
    parser.add_argument('--arms', default='B0,B1,B2,B3,B4')
    parser.add_argument('--cases', help='explicit subset for diagnostics; defaults to all 60 frozen cases')
    parser.add_argument('--output', type=Path)
    parser.add_argument('--verify', type=Path, help='read-only verification of a completed output directory')
    args = parser.parse_args(argv)
    if args.verify:
        print(json.dumps(verify_output(args.verify.resolve())))
        return 0
    config = load_config(ROOT / 'eval/config/arms.yaml')
    arms = args.arms.split(',')
    if len(arms) != len(set(arms)) or any(arm not in TREATMENTS for arm in arms):
        parser.error('unknown or duplicate arm')
    validation = subprocess.run([sys.executable, 'eval/oracle/validate_manifest.py'], cwd=ROOT, capture_output=True)
    if validation.returncode or digest((ROOT / 'eval/dataset_manifest.json').read_bytes()) != MANIFEST:
        raise ValueError('FROZEN_MANIFEST_INVALID')
    cases = json.loads((ROOT / 'eval/cases/offline-60.json').read_text())
    if args.cases:
        requested = args.cases.split(',')
        if len(requested) != len(set(requested)) or not set(requested) <= {case['case_id'] for case in cases}:
            parser.error('unknown or duplicate offline case')
        cases = [case for case in cases if case['case_id'] in requested]
    inputs = json.loads((ROOT / 'fixtures/datasets/v1/inputs.json').read_text())
    truths = json.loads((ROOT / 'eval/oracle/v1_truth.json').read_text())
    name = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '-' + uuid.uuid4().hex[:8]
    output = (args.output or ROOT / 'evidence/experiments' / name).resolve()
    if not any(output.is_relative_to(ROOT / allowed) for allowed in ('evidence/experiments', '.local/eval-runs')):
        parser.error('output must be a new directory under evidence/experiments or .local/eval-runs')
    output.mkdir(parents=True, exist_ok=False, mode=0o700)
    (output / 'traces').mkdir()
    (output / 'raw').mkdir()
    for name in ['runs', 'model_calls', 'tool_calls', 'oracle_judgments', 'timings']:
        (output / (name + '.jsonl')).touch(exist_ok=False)
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    sys.path.insert(0, str(ROOT / 'scripts'))
    from source_fingerprint import fingerprint
    save_new(output / 'assignment.json', {'config': config, 'config_sha256': digest(canonical(config)),
             'manifest_sha256': MANIFEST, 'cases': cases, 'arms': arms, 'git_base_commit': revision,
             'source_fingerprint': fingerprint(), 'platform': platform.platform(),
             'cpu_count': os.cpu_count(), 'python': platform.python_version(),
             'sandbox_image': (ROOT / '.local/sandbox-image.id').read_text().strip(),
             'go': subprocess.check_output(['go', 'version'], text=True).strip(),
             'command': sys.argv, 'full_offline_assignment': len(cases) == 60,
             'usage_precision': 'local_stub_utf8_bytes_div4', 'seed_source': 'frozen_case',
             'latency_instrument': 'time.monotonic_ns; executor plus durable record; external oracle included and separately timed'})
    all_rows = []
    namespaces, artifact_namespaces = set(), set()
    with tempfile.TemporaryDirectory(prefix='eval-bin-', dir=ROOT / '.local') as build:
        binary = str(Path(build) / 'mgareval')
        subprocess.run(['go', 'build', '-o', binary, './cmd/mgareval'], cwd=ROOT, check=True)
        for case in cases:
            for arm in arms:
                start = time.monotonic_ns()
                case_id = case['case_id']
                stem = case_id + '-' + arm
                report, infrastructure, code = {}, None, None
                raw, stderr = b'', b''
                try:
                    completed = subprocess.run([binary, '--provider', 'mock', '--arm', arm, '--case', case_id],
                                               cwd=ROOT, capture_output=True, timeout=275)
                    raw, stderr, code = completed.stdout, completed.stderr, completed.returncode
                    report = json.loads(raw)
                    if code:
                        infrastructure = 'executor_nonzero'
                    else:
                        validate_observed(report, case, arm, config)
                        namespace, artifact_namespace = report['database_namespace'], report['artifact_namespace']
                        if (namespace in namespaces or artifact_namespace in artifact_namespaces
                                or not namespace or not artifact_namespace):
                            raise ValueError('REUSED_OR_EMPTY_NAMESPACE')
                        namespaces.add(namespace)
                        artifact_namespaces.add(artifact_namespace)
                except subprocess.TimeoutExpired as exc:
                    raw, stderr = exc.stdout or b'', exc.stderr or b''
                    infrastructure = 'executor_timeout'
                except (ValueError, KeyError, TypeError) as exc:
                    infrastructure = 'invalid_executor_report:' + type(exc).__name__
                    if not isinstance(report, dict):
                        report = {}
                except OSError:
                    infrastructure = 'executor_start_failure'
                raw_hash = archive(output / 'raw' / (stem + '.json.gz'), raw)
                if stderr:
                    archive(output / 'raw' / (stem + '.stderr.gz'), stderr)
                result = report.get('result') or {}
                trace = result.get('otlp_trace')
                trace_hash = archive(output / 'traces' / (stem + '.json.gz'), canonical(trace))
                for field, filename in [('model_calls', 'model_calls.jsonl'), ('tool_calls', 'tool_calls.jsonl')]:
                    for call in report.get(field) or []:
                        append_record(output / filename, {'arm': arm, 'case_id': case_id, **call})
                judgment = judge(inputs[case_id], truths[case_id], report.get('answer'))
                execution = report.get('execution_oracle') or {}
                judgment['execution_verified'] = execution.get('passed') is True
                judgment['oracle_pass'] = (judgment['semantic_pass'] and judgment['execution_verified']
                                          and not infrastructure)
                append_record(output / 'oracle_judgments.jsonl', {'arm': arm, 'case_id': case_id,
                              'run_id': report.get('run_id'), **judgment, 'execution_oracle': execution})
                known = [call for call in report.get('model_calls') or [] if call['usage_known']]
                row = {'case_id': case_id, 'arm': arm, 'seed': case['seed'], 'category': case['category'],
                       'run_id': report.get('run_id'), 'execution_status': 'failed' if infrastructure else report.get('execution_status', 'failed'),
                       'workflow_success': report.get('workflow_success'), 'oracle_pass': judgment['oracle_pass'],
                       'semantic_pass': judgment['semantic_pass'], 'execution_verified': judgment['execution_verified'],
                       'answer': report.get('answer'), 'success_claim': (report.get('answer') or {}).get('decision') == 'answer',
                       'infrastructure_error': infrastructure, 'exit_code': code,
                       'model_calls': len(report.get('model_calls') or []), 'tool_calls': len(report.get('tool_calls') or []),
                       'known_usage_calls': len(known), 'input_tokens_known': sum(c['usage'].get('input_tokens', 0) for c in known),
                       'output_tokens_known': sum(c['usage'].get('output_tokens', 0) for c in known),
                       'cost_micros_known': sum(c['usage'].get('cost_micros', 0) for c in known),
                       'parallel_workers': result.get('parallel_workers'), 'plan_count': len(report.get('business_plans', [])),
                       'replans': report.get('replans'), 'duplicate_deliveries': report.get('duplicate_deliveries'),
                       'durable_workflow_latency_ms': report.get('durable_workflow_latency_ms'),
                       'execution_oracle_ms': execution.get('elapsed_ms'),
                       'elapsed_before_record_ms': (time.monotonic_ns() - start) / 1_000_000,
                       'raw': 'raw/' + stem + '.json.gz', 'raw_sha256': raw_hash, 'trace_sha256': trace_hash}
                append_record(output / 'runs.jsonl', row)
                append_record(output / 'timings.jsonl', {'case_id': case_id, 'arm': arm,
                              'end_to_end_recorded_ms': (time.monotonic_ns() - start) / 1_000_000})
                all_rows.append(row)
                print(json.dumps({'completed': len(all_rows), 'assigned': len(cases) * len(arms), 'case': case_id,
                                  'arm': arm, 'oracle_pass': judgment['oracle_pass'], 'error': infrastructure}), flush=True)
    validate_complete(all_rows, [case['case_id'] for case in cases], arms)
    summary = summarize(all_rows, arms)
    if fingerprint() != json.loads((output / 'assignment.json').read_text())['source_fingerprint']:
        summary['source_changed_during_run'] = True
        save_new(output / 'summary.json', summary)
        raise ValueError('SOURCE_CHANGED_DURING_EXPERIMENT')
    summary['integrity'] = verify_output(output)
    save_new(output / 'summary.json', summary)
    with (output / 'per_case.csv').open('x', newline='') as stream:
        fields = ['case_id', 'arm', 'category', 'execution_status', 'oracle_pass', 'model_calls', 'tool_calls', 'infrastructure_error']
        writer = csv.DictWriter(stream, fieldnames=fields, extrasaction='ignore')
        writer.writeheader()
        writer.writerows(all_rows)
    histogram = {}
    timing_rows = [json.loads(line) for line in (output / 'timings.jsonl').read_text().splitlines()]
    bounds = [1000, 5000, 10000, 30000, 60000, 120000, 300000]
    for arm in arms:
        values = [row['end_to_end_recorded_ms'] for row in timing_rows if row['arm'] == arm]
        histogram[arm] = {str(upper): sum(lower <= value < upper for value in values)
                          for lower, upper in zip([0] + bounds[:-1], bounds, strict=True)}
        histogram[arm]['>=300000'] = sum(value >= 300000 for value in values)
    save_new(output / 'latency_histogram.json', {'unit': 'ms', 'bins_upper_exclusive': bounds,
             'instrument': 'time.monotonic_ns through durable runs.jsonl append; includes external oracle', 'arms': histogram})
    taxonomy = ['# Actual failures retained', '', '| Arm | Assigned | Oracle failures | Execution failures | Infrastructure failures |',
                '|---|---:|---:|---:|---:|']
    for arm, values in summary['arms'].items():
        taxonomy.append(f"| {arm} | {values['assigned']} | {values['oracle_fail']} | {values['execution_failures']} | {values['infrastructure_failures']} |")
    taxonomy += ['', 'Every assigned case, including budget denial and unknown tool outcomes, remains in runs.jsonl.',
                 'Oracle failures are measured experimental outcomes; they are not deleted to make engineering acceptance pass.']
    (output / 'failure_taxonomy.md').write_text('\n'.join(taxonomy) + '\n')
    print(json.dumps({'output': str(output), 'summary': summary}), flush=True)
    return int(any(row['infrastructure_error'] for row in all_rows))


if __name__ == '__main__':
    raise SystemExit(main())
