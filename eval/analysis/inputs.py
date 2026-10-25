"""Read-only, hash-pinned E0 evidence ingestion."""
import gzip
import json
from pathlib import Path

from eval.runner import ROOT, TREATMENTS, digest, validate_complete, verify_output


def local_path(path, *, root=ROOT):
    path = Path(path)
    if not path.is_absolute():
        path = root / path
    if '..' in path.parts or any(p.is_symlink() for p in [path, *path.parents]):
        raise ValueError('UNSAFE_EVIDENCE_PATH')
    if not path.resolve().is_relative_to(root.resolve()):
        raise ValueError('EXTERNAL_EVIDENCE_PATH')
    return path


def information(report, case):
    result = dict.fromkeys(['critical_admitted', 'critical_available', 'citation_valid', 'citation_total',
                           'business_roles_retained', 'business_roles_required'], 0)
    artifacts = report['result'].get('candidates', []) or []
    by_id = {a['artifact_id']: a for a in artifacts}
    for selection in report.get('candidate_selections', []) or []:
        ids = [v['artifact_id'] for k in ['admitted', 'rejected'] for v in selection[k]]
        if len(ids) != len(set(ids)) or any(i not in by_id for i in ids):
            raise ValueError('UNKNOWN_OR_DUPLICATE_SELECTION_ID')
        for key in ['admitted', 'rejected']:
            count = sum(by_id[v['artifact_id']]['kind'] in ['finding', 'sql_evidence', 'patch'] for v in selection[key])
            result['critical_available'] += count
            if key == 'admitted':
                result['critical_admitted'] += count
    if report['arm'] == 'B0' or (report.get('answer') or {}).get('decision') != 'answer':
        return result
    strict = report['treatment']['internal_verifier']
    refs = []
    if strict:
        by_id = {a['metadata']['artifact_id']: a['metadata'] for a in report['result'].get('artifacts', []) or []}
        refs = [s for f in (report['result'].get('merge') or {}).get('facts', []) for s in f['sources']]
    else:
        aggregates = [w for w in report['result']['workers'] if w['role'] == 'aggregate' and w['status'] == 'completed']
        if aggregates:
            refs = aggregates[-1]['output'].get('citations', [])
    retained = set()
    roles = {t['task_id']: t['role'] for t in report['result']['audit']['tasks']}
    # Count citation occurrences (strict facts may cite the same artifact).
    for ref in refs:
        original = by_id.get(ref['artifact_id'], {})
        fields = ['artifact_id', 'content_sha256', 'producer_task_id', 'source_plan_version', 'kind', 'byte_length']
        valid = bool(original) and all(original.get(k) == ref.get(k) and ref.get(k) is not None for k in fields)
        valid = valid and original.get('producer_run_id') == report['run_id']
        if strict:
            valid = valid and all(original.get(k) == ref.get(k) for k in ['tool_schema_version', 'content_type'])
        if not strict:
            valid = valid and original == ref and digest(ref['content'].encode()) == ref['content_sha256']
        result['citation_total'] += 1
        result['citation_valid'] += valid
        if valid:
            retained.add(roles.get(ref['producer_task_id']))
    required = {'research', 'data'} | ({'action'} if case['business'] == 'incident' else set())
    result['business_roles_required'] = len(required)
    result['business_roles_retained'] = len(required & retained)
    return result


def load(input_dir):
    index_path = local_path(Path(input_dir) / 'index.json')
    index = json.loads(index_path.read_text())
    if index.get('schema_version') != 'analysis-input.v1' or not index.get('groups'):
        raise ValueError('INVALID_INPUT_INDEX')
    rows, assignments, seen_ids, namespaces = [], [], set(), set()
    for group in index['groups']:
        base = local_path(group['path'])
        inventory = group['files_sha256']
        observed = {str(p.relative_to(base)) for p in base.rglob('*') if p.is_file() or p.is_symlink()}
        if observed != inventory.keys():
            raise ValueError('INPUT_INVENTORY_CHANGED')
        for name, sha in inventory.items():
            path = local_path(base / name)
            if not path.is_relative_to(base) or not path.is_file() or digest(path.read_bytes()) != sha:
                raise ValueError('INPUT_HASH_CHANGED')
        verify_output(base)
        assignment = json.loads((base / 'assignment.json').read_text())
        if assignments and any(assignment[k] != assignments[0][k] for k in ['cases', 'config', 'source_fingerprint', 'manifest_sha256']):
            raise ValueError('CROSS_GROUP_CONFIGURATION_MISMATCH')
        assignments.append(assignment)
        timings = {(r['case_id'], r['arm']): r for r in read_lines(base / 'timings.jsonl')}
        models = read_lines(base / 'model_calls.jsonl')
        tools = read_lines(base / 'tool_calls.jsonl')
        cases = {c['case_id']: c for c in assignment['cases']}
        for run in read_lines(base / 'runs.jsonl'):
            key = (run['case_id'], run['arm'])
            if run['run_id'] in seen_ids:
                raise ValueError('REUSED_RUN_ID')
            seen_ids.add(run['run_id'])
            case = cases[run['case_id']]
            report = json.loads(gzip.decompress((base / run['raw']).read_bytes()))
            row = {'case_id': run['case_id'], 'arm': run['arm'], 'category': case['category'], 'run_id': run['run_id'],
                   'oracle_pass': run['oracle_pass'], 'success_claim': (run.get('answer') or {}).get('decision') == 'answer',
                   'workflow_success': run['workflow_success'], 'latency_ms': timings[key]['end_to_end_recorded_ms'],
                   'workflow_ms': run['durable_workflow_latency_ms'],
                   'models': [c for c in models if (c['case_id'], c['arm']) == key],
                   'tool_calls': sum((c['case_id'], c['arm']) == key for c in tools),
                   'execution_status': run['execution_status'], 'infrastructure_error': run['infrastructure_error']}
            if run['infrastructure_error']:
                # Keep allocation even if its detailed report is unavailable.
                row.update(dict.fromkeys(['critical_admitted', 'critical_available', 'citation_valid', 'citation_total',
                                          'business_roles_retained', 'business_roles_required'], 0))
            else:
                if {k: case[k] for k in ['case_id', 'category', 'seed', 'negative', 'limits']} != report['case'] or run['workflow_success'] != report['workflow_success']:
                    raise ValueError('RAW_CASE_OR_WORKFLOW_MISMATCH')
                if row['workflow_ms'] != report['durable_workflow_latency_ms']:
                    raise ValueError('RAW_WORKFLOW_TIMING_CHANGED')
                for artifact in report['result'].get('artifacts', []) or []:
                    meta, body = artifact['metadata'], artifact['body'].encode()
                    if digest(body) != meta['content_sha256'] or len(body) != meta['byte_length']:
                        raise ValueError('ARTIFACT_BODY_CHANGED')
                if row['tool_calls'] != len(report['tool_calls']):
                    raise ValueError('RAW_TOOL_COUNT_MISMATCH')
                if {c['call_id']: {k: v for k, v in c.items() if k not in ['arm', 'case_id']} for c in row['models']} != {c['call_id']: c for c in report['model_calls']}:
                    raise ValueError('RAW_USAGE_CHANGED')
                for field in ['database_namespace', 'artifact_namespace']:
                    namespace = report[field]
                    if namespace in namespaces:
                        raise ValueError('REUSED_NAMESPACE')
                    namespaces.add(namespace)
                row.update(information(report, case))
            rows.append(row)
    frozen = json.loads((ROOT / 'eval/cases/offline-60.json').read_text())
    frozen_cases = frozen['cases'] if isinstance(frozen, dict) else frozen
    if assignments[0]['cases'] != frozen_cases:
        raise ValueError('FROZEN_ASSIGNMENT_CHANGED')
    validate_complete(rows, [c['case_id'] for c in frozen_cases], list(TREATMENTS))
    return sorted(rows, key=lambda r: (r['case_id'], r['arm'])), {'index_sha256': digest(index_path.read_bytes()), 'assignments': assignments}


def read_lines(path):
    return [json.loads(line) for line in path.read_text().splitlines()]
