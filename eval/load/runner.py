"""Bounded E0 engineering load and eight-window process-fault acceptance."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import uuid

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from eval.analysis.inputs import local_path  # noqa: E402
from eval.analysis.metrics import percentile  # noqa: E402
from eval.runner import digest  # noqa: E402
from scripts.verify_results import go_result  # noqa: E402


def host():
    result = {'platform': platform.platform(), 'cpu_visible': os.cpu_count(), 'python': platform.python_version()}
    for name in ['cpu.max', 'memory.max', 'memory.current', 'memory.peak']:
        path = Path('/sys/fs/cgroup') / name
        result[name] = path.read_text().strip() if path.exists() else None
    return result


def guard(environment):
    if platform.system() != 'Linux' or os.environ.get('MGAR_ALLOW_PAID', '0') != '0':
        raise ValueError('LINUX_OFFLINE_REQUIRED')
    limit, used = environment.get('memory.max'), environment.get('memory.current')
    if limit and used and limit != 'max' and int(limit) - int(used) < 2 * 1024**3:
        raise ValueError('LESS_THAN_2_GIB_HEADROOM')


def parse_records(events, marker):
    # go test -json splits long log lines across Output frames.
    streams = {}
    for event in events:
        key = (event.get('Package'), event.get('Test'))
        streams.setdefault(key, []).append(event.get('Output', ''))
    records = []
    for chunks in streams.values():
        for line in ''.join(chunks).splitlines():
            if marker + ' ' in line:
                records.append(json.loads(line.split(marker + ' ', 1)[1]))
    return records


def validate_load(records):
    if sorted(r['concurrency'] for r in records) != [1, 4, 8, 16]:
        raise ValueError('MISSING_OR_DUPLICATE_LOAD_LEVEL')
    result, all_ids, namespaces = [], set(), set()
    for record in sorted(records, key=lambda r: r['concurrency']):
        n = record['concurrency']
        obs = record['worker_observations']
        ids = [r['task_id'] for r in obs]
        pids = {r['pid'] for r in obs}
        if (record['assigned'] != n * 4 or record['completed'] != n * 4 or record['failed'] != 0
                or record['model_calls'] != n * 4 or len(ids) != n * 4 or len(set(ids)) != n * 4
                or len(pids) != n or any(sum(r['pid'] == pid for r in obs) != 4 for pid in pids)
                or set(ids) & all_ids or record['namespace'] in namespaces
                or record['terminal_events'] != n or record['duplicate_outputs'] or record['resource_leaks']):
            raise ValueError('LOAD_IDENTITY_OR_INVARIANT_FAILURE')
        all_ids.update(ids)
        namespaces.add(record['namespace'])
        if not 1 <= record['resource_capacity'] <= 4 or not record['samples']:
            raise ValueError('MISSING_RESOURCE_OBSERVATIONS')
        if any(s['max_class_held'] > record['resource_capacity'] or s['queue'] < 0 for s in record['samples']):
            raise ValueError('RESOURCE_CAPACITY_EXCEEDED')
        runs = {r['run_id'] for r in obs}
        if len(runs) != n or len(record['workflows']) != n or {w['run_id'] for w in record['workflows']} != runs:
            raise ValueError('MISSING_WORKFLOW')
        for workflow in record['workflows']:
            b = workflow['budget']
            if (workflow['state'] != 'succeeded' or b['conserved'] is not True or b['settled_models'] != 4
                    or b['reserved_models'] != 0 or b['settled_cost_micros'] != 0):
                raise ValueError('BUDGET_OR_TERMINAL_FAILURE')
        if not 0 < record['execution_ms'] <= record['including_verification_ms']:
            raise ValueError('INVALID_LOAD_TIMING')
        values = [o['latency_ms'] for o in obs]
        result.append({'concurrency': n, 'assigned': n * 4, 'completed': record['completed'],
                       'failed': record['failed'], 'resource_capacity': record['resource_capacity'],
                       'claim_to_output_p50_ms': percentile(values, 50), 'claim_to_output_p95_ms': percentile(values, 95),
                       'worker_execution_tasks_per_second': n * 4000 / record['execution_ms'],
                       'including_verification_tasks_per_second': n * 4000 / record['including_verification_ms'],
                       'observed_queue_peak': max(s['queue'] for s in record['samples']),
                       'observed_db_connections_peak': max(s['database_connections'] for s in record['samples']),
                       'observed_db_active_peak': max(s['database_active'] for s in record['samples']),
                       'observed_db_ungranted_locks_peak': max(s['database_ungranted_locks'] for s in record['samples']),
                       'database_bytes_after': record['database_bytes_after']})
    return result


def validate_kills(kills, recoveries):
    expected = {'F-' + letter for letter in 'ABCDEFGH'}
    if len(kills) != 8 or {r['point'] for r in kills} != expected:
        raise ValueError('EIGHT_DISTINCT_KILLS_REQUIRED')
    if {r['point'].split('/')[-1] for r in recoveries} != expected or len(recoveries) != 8:
        raise ValueError('EIGHT_RECOVERIES_REQUIRED')
    for kill in kills:
        if (kill['signal'] != 'SIGKILL' or kill['owned_pid'] < 2 or kill['process_group'] != kill['owned_pid']
                or not kill['before']['run_id'] or kill['before']['run_id'] != kill['after_death']['run_id']):
            raise ValueError('INVALID_DEATH_PROOF')
        recovery = next(r for r in recoveries if r['point'].split('/')[-1] == kill['point'])
        if recovery['run_id'] != kill['before']['run_id']:
            raise ValueError('RECOVERY_RUN_MISMATCH')
    return sorted(kills, key=lambda r: r['point'])


def run(mode):
    before = host()
    guard(before)
    output = local_path(ROOT / 'evidence/eval/engineering' / (mode + '-' + uuid.uuid4().hex))
    output.mkdir(parents=True)
    selector = '^TestOfflineLoad$' if mode == 'load' else '^(TestKillMatrixCore|TestArtifactStageCrash|TestUnknownModelCrash|TestVerifiedRecoveryCrash)$'
    command = ['go', 'test', '-count=1', '-timeout=240s', '-json', './tests/chaos/...', '-run', selector]
    fingerprint = subprocess.check_output([sys.executable, 'scripts/source_fingerprint.py'], cwd=ROOT, text=True).strip()
    start = datetime.now(timezone.utc).isoformat()
    try:
        process = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=270)
        raw, stderr, code = process.stdout, process.stderr, process.returncode
    except subprocess.TimeoutExpired as exc:
        raw = (exc.stdout or b'').decode() if isinstance(exc.stdout, bytes) else (exc.stdout or '')
        stderr, code = 'bounded harness timed out', 124
    (output / 'go.jsonl').write_text(raw)
    (output / 'stderr.log').write_text(stderr)
    report = {'schema_version': 'engineering-experiment.v1', 'evidence_level': 'E0_MOCK_ENGINEERING',
              'mode': mode, 'command': command, 'exit_code': code, 'started_utc': start,
              'source_fingerprint': fingerprint, 'environment_before': before, 'environment_after': host(),
              'status': 'FAIL', 'paid_calls': 0, 'physical_host_failure': 'NOT_RUN',
              'limitations': ['Single host only; no power loss, partition, real provider or production traffic.',
                              'Load uses trusted Go Worker processes, fixed SQL/CPU work and local model stub.',
                              'Queue/DB metrics sampled every 10 ms; short peaks may be missed.',
                              'DB resource metrics include other activity in this owned synthetic database.']}
    try:
        if code:
            raise ValueError('GO_HARNESS_FAILED')
        events = [json.loads(line) for line in raw.splitlines()]
        report['go'] = go_result(events)
        if report['go']['status'] != 'PASS':
            raise ValueError('INCOMPLETE_OR_SKIPPED_TESTS')
        if mode == 'load':
            records = parse_records(events, 'LOAD_REPORT')
            report['summary'] = validate_load(records)
            report['raw_measurements'] = records
        else:
            recoveries = parse_records(events, 'CHAOS_RECOVERY')
            report['kills'] = validate_kills(parse_records(events, 'CHAOS_KILL'), recoveries)
            report['recoveries'] = recoveries
        if fingerprint != subprocess.check_output([sys.executable, 'scripts/source_fingerprint.py'], cwd=ROOT, text=True).strip():
            raise ValueError('SOURCE_CHANGED_DURING_RUN')
        report['status'] = 'PASS'
    except (ValueError, KeyError, TypeError) as exc:
        report['error'] = str(exc)
    report['raw_sha256'] = digest(raw.encode())
    (output / 'report.json').write_text(json.dumps(report, sort_keys=True, indent=2, allow_nan=False) + '\n')
    lines = ['# E0 engineering ' + mode, '', 'Status: ' + report['status'], '']
    if 'summary' in report:
        keys = list(report['summary'][0])
        lines += ['| ' + ' | '.join(keys) + ' |', '| ' + ' | '.join(['---'] * len(keys)) + ' |']
        for row in report['summary']:
            lines += ['| ' + ' | '.join(f'{row[k]:.3f}' if isinstance(row[k], float) else str(row[k]) for k in keys) + ' |']
    lines += ['', *['- ' + limit for limit in report['limitations']], '']
    (output / 'report.md').write_text('\n'.join(lines))
    print(json.dumps({'output': str(output.relative_to(ROOT)), 'status': report['status'], 'error': report.get('error')}))
    return 0 if report['status'] == 'PASS' else 1


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--mode', choices=['load', 'kill'], required=True)
    raise SystemExit(run(parser.parse_args().mode))
