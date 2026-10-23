"""P2-05 trusted synthetic patches driven by actual Go observations."""
import json
import hashlib
from pathlib import Path
from uuid import uuid4

import pytest

from mgar_agents.action import ActionAgent, ActionWorkspace
from mgar_agents.core.claim import Claim
from mgar_agents.core.provider import MockProvider
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task


def task_input(role='action'):
    claim = Claim(str(uuid4()), str(uuid4()), 'fixture-action-worker', role, 1, 0, 1)
    task = Task(claim, 'Deduplicate paid orders; retain actual failing and passing tests', 'incident-action')
    # Trusted handoff fixture, not a claim that P3 CAS/authorized retrieval exists.
    upstream = 'Synthetic upstream evidence: duplicate order 101; paid deduplicated total 150.'
    snapshot = ContextSnapshot.from_dict({'schema_version': 'context.v1', 'task_id': claim.task_id,
        'plan_version': 1, 'model_config_version': 'mock.v1', 'tool_policy_version': 'action_fixture.v1',
        'artifacts': [{'artifact_id': str(uuid4()), 'content_sha256': hashlib.sha256(upstream.encode()).hexdigest(),
            'purpose': upstream}]})
    return task, snapshot


def tool(turn, name, arguments):
    return json.dumps({'type': 'tool_call', 'call_id': f'call-{turn}', 'name': name, 'arguments': arguments})


def choose_patch(request):
    if not request.observations:
        assert 'paid deduplicated total 150' in request.snapshot_json
        return tool(request.turn, 'action.inspect', {})
    last = request.observations[-1]
    source = last.output
    if last.name in {'action.inspect', 'action.apply_patch'}:
        return tool(request.turn, 'action.go_test', {'expected_workspace_sha': source['workspace_sha256']})
    if not source['passed']:
        patch = 'deduplicate-only' if source['patch_id'] == 'baseline' else 'deduplicate-paid'
        return tool(request.turn, 'action.apply_patch', {'expected_workspace_sha': source['workspace_sha256'],
            'path': 'aggregate.go', 'patch_id': patch, 'expected_file_sha256': source['files']['aggregate.go']})
    return json.dumps({'type': 'final', 'output': {'summary': 'The reviewed paid-order fix passes actual tests.',
        'patch_id': source['patch_id'], 'test_call_id': last.call_id,
        'workspace_sha256': source['workspace_sha256'], 'passed': True}})


def test_action_observes_failed_patch_then_selects_real_fix(tmp_path):
    task, snapshot = task_input()
    provider = MockProvider(choose_patch)
    with ActionWorkspace(task) as work:
        root = work.root
        result = ActionAgent(provider, work).run(task, snapshot, Budget(max_turns=8, max_tool_calls=7, timeout_s=90))
        assert result.status == 'completed' and result.turns == 7 and result.tool_calls == 6
        observed = [o.output for o in result.observations if o.name == 'action.go_test']
        assert [o['exit_code'] for o in observed] == [1, 1, 0]
        assert [o['passed'] for o in observed] == [False, False, True]
        assert ['got=320', 'got=220', 'got=150'] == [o['ground_truth_line'] for o in observed]
        assert len({o['workspace_sha256'] for o in observed}) == 3
        assert all(o['tests_run'] == ['TestPaidDeduplicatedTotal'] and o['skipped'] == 0 for o in observed)
        assert all(Path(o['execution_log']).is_file() for o in observed)
        assert all(json.loads(Path(o['execution_log']).read_text())['raw_go_json'] for o in observed)
        assert result.output['verified'] is False and result.verified is False
        assert result.output['persisted'] is False and result.output['artifact_uri'].startswith('artifact://')
        assert result.output['producer_task_id'] == task.claim.task_id
        assert result.output['input_snapshot_sha256'] == snapshot.content_sha256
        assert result.output['patch']['path'] == 'aggregate.go' and result.output['patch']['patch_id'] == 'deduplicate-paid'
        assert '-\t\ttotal += order.Amount' in result.output['patch']['diff']
        assert result.events[-1].kind == 'action_proposal_prepared'
    assert not root.exists()


@pytest.mark.parametrize('path', ['../escape.go', '/tmp/escape.go', 'go.mod', 'aggregate_test.go', 'aggregate.go;touch x'])
def test_action_patch_path_is_finite(tmp_path, path):
    task, _ = task_input()
    with ActionWorkspace(task, audit_dir=tmp_path/'audit') as work:
        before = work.inspect()
        with pytest.raises(PermissionError):
            work.apply_patch({'expected_workspace_sha': work.workspace_sha, 'path': path,
                'patch_id': 'deduplicate-paid', 'expected_file_sha256': before['files']['aggregate.go']})
        assert work.inspect() == before


@pytest.mark.parametrize('change', ['hash', 'workspace', 'patch', 'extra'])
def test_action_rejects_stale_or_arbitrary_patch(tmp_path, change):
    task, _ = task_input()
    with ActionWorkspace(task, audit_dir=tmp_path/'audit') as work:
        args = {'expected_workspace_sha': work.workspace_sha, 'path': 'aggregate.go', 'patch_id': 'deduplicate-paid',
            'expected_file_sha256': work.inspect()['files']['aggregate.go']}
        if change == 'hash':
            args['expected_file_sha256'] = '0'*64
        if change == 'workspace':
            args['expected_workspace_sha'] = '0'*64
        if change == 'patch':
            args['patch_id'] = 'arbitrary model code'
        if change == 'extra':
            args['content'] = 'package aggregate; import "os/exec"'
        with pytest.raises(PermissionError):
            work.apply_patch(args)


@pytest.mark.parametrize('kind', ['symlink', 'test-change', 'extra', 'hardlink'])
def test_action_workspace_inventory_and_hashes_are_rechecked(tmp_path, kind):
    task, _ = task_input()
    outside = tmp_path/'outside'
    outside.write_text('untouched')
    with ActionWorkspace(task, audit_dir=tmp_path/'audit') as work:
        digest = work.workspace_sha
        if kind == 'symlink':
            (work.root/'aggregate.go').unlink()
            (work.root/'aggregate.go').symlink_to(outside)
        elif kind == 'test-change':
            (work.root/'aggregate_test.go').write_text('package aggregate')
        elif kind == 'extra':
            (work.root/'evil.go').write_text('package aggregate')
        else:
            import os
            os.link(work.root/'aggregate.go', tmp_path/'linked')
        with pytest.raises(PermissionError):
            work.go_test({'expected_workspace_sha': digest}, timeout_s=5)
        assert outside.read_text() == 'untouched'


@pytest.mark.parametrize('kind', ['no-test', 'fake-pass', 'verified', 'wrong-call', 'wrong-hash'])
def test_action_final_claim_cannot_forge_actual_test_success(tmp_path, kind):
    task, snapshot = task_input()
    def decide(request):
        if kind != 'no-test' and request.turn == 1:
            return tool(1, 'action.go_test', {'expected_workspace_sha': work.workspace_sha})
        body = {'summary': 'claimed pass', 'patch_id': 'deduplicate-paid', 'test_call_id': 'call-1',
            'workspace_sha256': work.workspace_sha, 'passed': True}
        if kind == 'verified':
            body['verified'] = True
        if kind == 'wrong-call':
            body['test_call_id'] = 'foreign-call'
        if kind == 'wrong-hash':
            body['workspace_sha256'] = '0'*64
        return json.dumps({'type': 'final', 'output': body})
    with ActionWorkspace(task, audit_dir=tmp_path/'audit') as work:
        if kind in {'verified', 'wrong-call', 'wrong-hash'}:
            work.apply_patch({'expected_workspace_sha': work.workspace_sha, 'path': 'aggregate.go',
                'patch_id': 'deduplicate-paid', 'expected_file_sha256': work.inspect()['files']['aggregate.go']})
        result = ActionAgent(MockProvider(decide), work).run(task, snapshot, Budget(timeout_s=45))
        assert result.status == 'failed' and result.error_kind == 'evidence_rejected'
        assert result.output is None and result.events[-1].kind == 'run_failed'
        if kind in {'verified', 'wrong-call', 'wrong-hash'}:
            assert result.observations[0].output['passed'] is True


def test_action_identity_role_and_command_arguments_denied(tmp_path):
    task, snapshot = task_input('data')
    with pytest.raises(PermissionError):
        ActionWorkspace(task, audit_dir=tmp_path/'audit')
    task, snapshot = task_input()
    with ActionWorkspace(task, audit_dir=tmp_path/'audit') as work:
        other, other_snapshot = task_input()
        provider = MockProvider(choose_patch)
        result = ActionAgent(provider, work).run(other, other_snapshot, Budget())
        assert result.error_kind == 'policy_denied' and not provider.requests
        attack = MockProvider([tool(1, 'action.go_test', {'expected_workspace_sha': work.workspace_sha,
            'command': 'go test; touch /tmp/forged'})])
        result = ActionAgent(attack, work).run(task, snapshot, Budget())
        assert result.error_kind == 'policy_denied' and result.observations == ()


def test_action_real_go_timeout_retains_failed_execution(tmp_path):
    task, _ = task_input()
    with ActionWorkspace(task, audit_dir=tmp_path/'audit') as work:
        result = work.go_test({'expected_workspace_sha': work.workspace_sha}, timeout_s=0.000001)
        assert result['passed'] is False and result['stop_reason'] == 'timeout'
        assert result['exit_code'] == -9
        raw = json.loads(Path(result['execution_log']).read_text())
        assert raw['exit_code'] == -9 and raw['workspace_sha256'] == work.workspace_sha


def test_action_real_go_output_is_capped_and_unsafe_env_not_inherited(tmp_path, monkeypatch):
    task, _ = task_input()
    monkeypatch.setenv('GOFLAGS', '-invalid-untrusted-environment-flag')
    monkeypatch.setenv('MGAR_ACTION_SECRET_PROBE', 'must-never-reach-child')
    with ActionWorkspace(task, audit_dir=tmp_path/'audit', max_output_bytes=256) as work:
        assert 'GOFLAGS' not in work._environment and 'MGAR_ACTION_SECRET_PROBE' not in work._environment
        result = work.go_test({'expected_workspace_sha': work.workspace_sha}, timeout_s=5)
        assert result['passed'] is False and result['stop_reason'] == 'output_limit'
        record = json.loads(Path(result['execution_log']).read_text())
        assert len(record['raw_go_json'].encode()) == 256
        assert record['argv'][1:] == ['test', '-count=1', '-json', './...']


@pytest.mark.parametrize('kind', ['symlink-root', 'changed-source', 'changed-test', 'oversized', 'extra-file'])
def test_action_fixture_is_trusted_fixed_content_only(tmp_path, kind):
    import shutil
    from mgar_agents.action.workspace import DEFAULT_FIXTURE
    fixture = tmp_path/'fixture'
    shutil.copytree(DEFAULT_FIXTURE, fixture)
    if kind == 'symlink-root':
        alias = tmp_path/'alias'
        alias.symlink_to(fixture, target_is_directory=True)
        fixture = alias
    elif kind == 'changed-source':
        (fixture/'aggregate.go').write_text('package aggregate\nimport "os/exec"')
    elif kind == 'changed-test':
        (fixture/'aggregate_test.go').write_text('package aggregate')
    elif kind == 'oversized':
        (fixture/'aggregate.go').write_bytes(b'x'*4097)
    else:
        (fixture/'extra.go').write_text('package aggregate')
    task, _ = task_input()
    with pytest.raises(PermissionError):
        ActionWorkspace(task, fixture_root=fixture, audit_dir=tmp_path/'audit')
