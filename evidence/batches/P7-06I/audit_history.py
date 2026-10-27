"""Read-only prior-trial integrity/reanalysis/PG audit; no generation or replay."""
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import shutil
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[3]
sys.path[:0] = [str(ROOT), str(ROOT / 'tests')]
from conftest import _admin_kwargs
from eval.live.analyze import analyze
import psycopg
from psycopg import sql


def main():
    checkpoint = json.loads(Path(__file__).with_name('historical-inputs.json').read_text())
    for name, sha in checkpoint['files'].items():
        assert hashlib.sha256((ROOT / name).read_bytes()).hexdigest() == sha, name
    manifest = json.loads((ROOT / 'eval/dataset_manifest.json').read_text())
    frozen_sources = json.loads((ROOT / 'tests/fixtures/round2-protocol.json').read_text())['files']
    trials = [('P7-06-deepseek-flash-1017-20261026', 'actual-20261027', 'original'),
              ('P7-06-round2-deepseek-flash-1017-20261027', 'round2-results-20261027', 'round2')]
    results = []
    with tempfile.TemporaryDirectory(prefix='history-readonly-', dir=ROOT / '.local') as directory:
        history = Path(directory)
        names = set(manifest['files']) | {'eval/dataset_manifest.json', 'eval/live/plan.json', 'eval/live/round2/plan.json',
            'evidence/eval/live/P7-06-deepseek-flash-1017-20261026/report.json'} | set(frozen_sources)
        for name in names:
            target = history / name
            assert target.is_relative_to(history)
            target.parent.mkdir(parents=True, exist_ok=True)
            if name in frozen_sources:
                target.write_text(frozen_sources[name])
            else:
                shutil.copyfile(ROOT / name, target)
        for experiment, report, selector in trials:
            actual = analyze(ROOT / 'evidence/eval/live' / experiment, root=history, round_name=selector)
            expected = json.loads((ROOT / 'evidence/eval/live' / report / 'metrics.json').read_text())
            assert actual == expected, experiment + ': metrics changed'
            results.append({'experiment_id': experiment, 'metrics_identical': True,
                            'model_calls': actual['recorded_model_calls'], 'upper_cost_micros': actual['upper_cost_micros']})
    kwargs = _admin_kwargs()
    kwargs['options'] = '-c default_transaction_read_only=on -c statement_timeout=5000'
    fields = ['call_id', 'logical_call_id', 'run_id', 'task_id', 'provider', 'model', 'status',
              'usage', 'usage_known', 'usage_source', 'reservation', 'settlement']
    checked = models = tools = unknown_tools = 0
    with psycopg.connect(**kwargs) as conn:
        conn.execute('SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY')
        for experiment, _, _ in trials:
            journal = ROOT / 'evidence/eval/live' / experiment
            for path in sorted(journal.glob('*.raw.json')):
                raw = json.loads(path.read_text())
                namespace = raw['database_namespace']
                assert re.fullmatch(r'mgar_demo_[0-9a-f]{16}', namespace)
                query = sql.SQL('SELECT to_jsonb(m) FROM {}.model_calls m WHERE run_id=%s').format(sql.Identifier(namespace))
                actual_models = {row[0]['call_id']: {key: row[0][key] for key in fields} for row in conn.execute(query, (raw['run_id'],))}
                expected_models = {call['call_id']: {key: call[key] for key in fields} for call in raw.get('model_calls') or []}
                assert actual_models == expected_models, 'prior model rows changed'
                query = sql.SQL("SELECT jsonb_build_object('invocation_id',v.invocation_id,'run_id',t.run_id,'task_id',v.task_id,'capability',v.capability,'request_hash',v.request_hash,'effect',v.effect_class,'tool_version',v.scope->>'Version','plan_version',v.scope->'Plan','status',v.status,'result_artifact_id',v.result_artifact_id) FROM {}.tool_invocations v JOIN {}.task_instances t USING(task_id) WHERE t.run_id=%s").format(sql.Identifier(namespace), sql.Identifier(namespace))
                actual_tools = {row[0]['invocation_id']: row[0] for row in conn.execute(query, (raw['run_id'],))}
                assert actual_tools == {tool['invocation_id']: tool for tool in raw.get('tool_calls') or []}, 'prior tool rows changed'
                query = sql.SQL('SELECT (settled_cost*1000000)::bigint,(reserved_cost*1000000)::bigint FROM {}.run_budgets WHERE run_id=%s').format(sql.Identifier(namespace))
                costs = conn.execute(query, (raw['run_id'],)).fetchone()
                budget = raw['result']['budget']
                assert costs == (budget['settled_cost_micros'], budget['reserved_cost_micros'])
                checked += 1
                models += len(actual_models)
                tools += len(actual_tools)
                unknown_tools += sum(tool['status'] == 'unknown' for tool in actual_tools.values())
        markers = []
        for run, epoch, revision in [('92d7ff2b-cf3d-5ec3-a1a0-c938f24aa4a7', 2, 1), ('1e69bf79-5997-5fa6-84b2-e509f1d953b8', 1, 0)]:
            row = conn.execute('SELECT w.state,l.epoch,l.revision FROM public.workflow_runs w JOIN public.orchestrator_leases l USING(run_id) WHERE run_id=%s', (run,)).fetchone()
            assert row == ('cancelled', epoch, revision)
            markers.append({'run_id': run, 'state': row[0], 'epoch': row[1], 'lease_revision': row[2]})
    assert models == 574 and unknown_tools == 1
    assert sum(row['upper_cost_micros'] for row in results) == 311229
    result = {'timestamp_utc': datetime.now(timezone.utc).isoformat(), 'status': 'UNCHANGED', 'read_only': True,
              'historical_file_hashes': len(checkpoint['files']), 'historical_analysis': results,
              'checked_assignments': checked, 'model_rows': models, 'tool_rows': tools,
              'old_unknown_tool_rows_retained': unknown_tools, 'cumulative_retained_upper_micros': 311229,
              'allocation_markers': markers, 'paid_calls_added': 0,
              'method': 'Re-evaluate immutable journals with the frozen source-data snapshot; no archived code executed and no old freeze hash updated.'}
    with Path(__file__).with_name('history-audit.json').open('x') as stream:
        stream.write(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(result))


if __name__ == '__main__':
    try:
        main()
    except (AssertionError, psycopg.Error, OSError, ValueError, KeyError, TypeError) as error:
        print(json.dumps({'status': 'AUDIT_ERROR', 'error_type': type(error).__name__,
                          'assertion': str(error) if isinstance(error, AssertionError) else 'details_withheld'}))
        raise SystemExit(1)
