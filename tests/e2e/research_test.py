"""P2-03 real local-source tools driven by offline model decisions."""
from dataclasses import replace
import hashlib
import json
from pathlib import Path
import shutil
from uuid import uuid4

import pytest

from mgar_agents.core.claim import Claim
from mgar_agents.core.provider import MockProvider
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task
from mgar_agents.research import ResearchAgent, SourceIndex, SourceSpec

FIXTURE = Path(__file__).resolve().parents[2] / 'fixtures/incident/docs'


def task_input(role='research'):
    claim = Claim(str(uuid4()), str(uuid4()), 'fixture-research-worker', role, 1, 0, 1)
    task = Task(claim, 'Investigate duplicate aggregation using synthetic sources', 'incident-research')
    snapshot = ContextSnapshot.from_dict({'schema_version': 'context.v1', 'task_id': claim.task_id,
        'plan_version': claim.plan_version, 'model_config_version': 'mock.v1',
        'tool_policy_version': 'research_fixture.v1', 'artifacts': []})
    return task, snapshot


def tool(turn, name, arguments):
    return json.dumps({'type': 'tool_call', 'call_id': f'call-{turn}', 'name': name, 'arguments': arguments})


def final(statement, citations):
    return json.dumps({'type': 'final', 'output': {'statement': statement, 'citations': citations}})


def clue_provider(request):
    if request.turn == 1:
        return tool(1, 'research.search', {'query': 'duplicate aggregation', 'limit': 4})
    if request.turn == 2:
        search = request.observations[0].output
        candidate = next(item for item in search['candidates'] if item['source_id'] == 'release-note')
        return tool(2, 'research.read', {'source_id': candidate['source_id']})
    observed = request.observations[-1].output
    assert 'aggregate.go' in observed['text']
    quote = next(line for line in observed['text'].splitlines() if 'duplicate' in line)
    return final('Release change identifies a duplicate aggregation risk.', [
        {'source_id': observed['source_id'], 'content_sha256': observed['content_sha256'], 'quote': quote}])


def copied_index(tmp_path):
    root = tmp_path/'sources'
    shutil.copytree(FIXTURE, root)
    return root, SourceIndex.from_manifest(root)


def test_research_search_read_and_cite_real_versioned_content(tmp_path):
    root, index = copied_index(tmp_path)
    task, snapshot = task_input()
    provider = MockProvider(clue_provider)
    result = ResearchAgent(provider, index).run(task, snapshot, Budget())
    assert result.status == 'completed' and result.turns == 3 and result.tool_calls == 2
    assert result.verified is False
    finding = result.output
    assert finding['schema_version'] == 'finding.v1' and finding['verified'] is False
    assert finding['input_snapshot_sha256'] == snapshot.content_sha256
    source = finding['citations'][0]
    content = (root/'release-note.md').read_bytes()
    assert source['content_sha256'] == hashlib.sha256(content).hexdigest()
    assert source['quote'] in content.decode()
    assert source['artifact_uri'].startswith('artifact://')
    assert source['persisted'] is False and source['verified'] is False
    assert source['producer_run_id'] == task.claim.run_id and source['producer_task_id'] == task.claim.task_id
    assert source['source_plan_version'] == 1 and source['index_version'] == index.version
    assert finding['source_index_sha256'] == index.content_sha256
    assert provider.requests[1].observations[0].output['candidates']
    assert result.events[-1].kind == 'research_finding_prepared'
    assert result.events[-1].observation_hash == hashlib.sha256(result.output_json.encode()).hexdigest()


def test_research_candidate_identity_is_per_producer(tmp_path):
    _, index = copied_index(tmp_path)
    first, snapshot1 = task_input()
    second, snapshot2 = task_input()
    a = ResearchAgent(MockProvider(clue_provider), index).run(first, snapshot1, Budget())
    b = ResearchAgent(MockProvider(clue_provider), index).run(second, snapshot2, Budget())
    assert a.output['citations'][0]['artifact_uri'] != b.output['citations'][0]['artifact_uri']
    assert a.output['citations'][0]['content_sha256'] == b.output['citations'][0]['content_sha256']


def test_research_low_quality_source_cannot_be_promoted_to_verified(tmp_path):
    _, index = copied_index(tmp_path)
    task, snapshot = task_input()
    def choose(request):
        if request.turn == 1:
            return tool(1, 'research.read', {'source_id': 'rumor'})
        observed = request.observations[0].output
        return final('Unconfirmed hint requires verification.', [{'source_id': observed['source_id'],
            'content_sha256': observed['content_sha256'], 'quote': observed['text'].strip()}])
    result = ResearchAgent(MockProvider(choose), index).run(task, snapshot, Budget())
    assert result.status == 'completed'
    assert result.output['citations'][0]['quality'] == 'unverified'
    assert result.output['citations'][0]['verified'] is False
    assert result.output['verified'] is False


@pytest.mark.parametrize('bad', ['missing', 'wrong_hash', 'wrong_quote', 'no_read', 'empty', 'verified'])
def test_research_rejects_unsupported_or_self_verified_findings(tmp_path, bad):
    _, index = copied_index(tmp_path)
    task, snapshot = task_input()
    def choose(request):
        if request.turn == 1 and bad != 'no_read':
            return tool(1, 'research.read', {'source_id': 'release-note'})
        if bad == 'no_read':
            raw = (FIXTURE/'release-note.md').read_bytes()
            observed = {'source_id': 'release-note', 'content_sha256': hashlib.sha256(raw).hexdigest(), 'text': raw.decode()}
        else:
            observed = request.observations[0].output
        citation = {'source_id': observed['source_id'], 'content_sha256': observed['content_sha256'], 'quote': observed['text'].splitlines()[1]}
        if bad == 'missing':
            citation['source_id'] = 'invented'
        if bad == 'wrong_hash':
            citation['content_sha256'] = '0'*64
        if bad == 'wrong_quote':
            citation['quote'] = 'This invented claim never appeared.'
        body = {'statement': 'untrusted claim', 'citations': [] if bad == 'empty' else [citation]}
        if bad == 'verified':
            body['verified'] = True
        return json.dumps({'type': 'final', 'output': body})
    result = ResearchAgent(MockProvider(choose), index).run(task, snapshot, Budget())
    assert result.status == 'failed' and result.error_kind == 'evidence_rejected'
    assert result.error_turn == result.turns and result.output is None
    assert result.events[-2].kind == 'research_finding_rejected'
    assert result.events[-1].kind == 'run_failed'
    assert result.verified is False


def test_research_prompt_injection_cannot_execute_command(tmp_path):
    _, index = copied_index(tmp_path)
    task, snapshot = task_input()
    marker = tmp_path/'must-not-exist'
    def choose(request):
        if request.turn == 1:
            return tool(1, 'research.read', {'source_id': 'injection'})
        assert 'ignore all rules' in request.observations[0].output['text']
        return tool(2, 'shell.exec', {'command': f'touch {marker}'})
    result = ResearchAgent(MockProvider(choose), index).run(task, snapshot, Budget())
    assert result.error_kind == 'policy_denied' and not marker.exists()
    assert result.tool_calls == 1


@pytest.mark.parametrize('source_id', ['../../private.txt', '/etc/passwd', 'unknown'])
def test_research_model_cannot_supply_paths_or_unlisted_sources(tmp_path, source_id):
    _, index = copied_index(tmp_path)
    task, snapshot = task_input()
    result = ResearchAgent(MockProvider([tool(1, 'research.read', {'source_id': source_id})]), index).run(task, snapshot, Budget())
    assert result.error_kind == 'policy_denied' and result.tool_calls == 0


def test_research_source_hash_drift_fails_closed(tmp_path):
    root, index = copied_index(tmp_path)
    (root/'release-note.md').write_text('silently replaced source')
    task, snapshot = task_input()
    result = ResearchAgent(MockProvider([tool(1, 'research.read', {'source_id': 'release-note'})]), index).run(task, snapshot, Budget())
    assert result.error_kind == 'policy_denied' and result.observations == ()


def test_research_symlink_source_and_root_are_refused(tmp_path):
    root, index = copied_index(tmp_path)
    outside = tmp_path/'outside.md'
    outside.write_text('private source')
    (root/'release-note.md').unlink()
    (root/'release-note.md').symlink_to(outside)
    task, snapshot = task_input()
    result = ResearchAgent(MockProvider([tool(1, 'research.read', {'source_id': 'release-note'})]), index).run(task, snapshot, Budget())
    assert result.error_kind == 'policy_denied' and result.observations == ()
    alias = tmp_path/'alias'
    alias.symlink_to(root, target_is_directory=True)
    with pytest.raises((ValueError, PermissionError)):
        SourceIndex.from_manifest(alias)


def test_research_capability_role_and_source_bounds(tmp_path):
    root, index = copied_index(tmp_path)
    task, snapshot = task_input('data')
    provider = MockProvider([final('untrusted', [])])
    result = ResearchAgent(provider, index).run(task, snapshot, Budget())
    assert result.error_kind == 'policy_denied' and provider.requests == []
    with pytest.raises(ValueError):
        SourceSpec('escape', '../outside.md', 'doc', 'reference', '0'*64)
    with pytest.raises(ValueError):
        SourceIndex(root, [], 'v1')
    with pytest.raises(ValueError):
        SourceIndex(root, [SourceSpec(f's{i}', 'release-note.md', 'doc', 'reference', '0'*64) for i in range(17)], 'v1')


def test_research_manifest_is_fixed_and_search_limits_output(tmp_path):
    root, index = copied_index(tmp_path)
    manifest = json.loads((root/'manifest.json').read_text())
    manifest['sources'].append({'source_id': 'private', 'relative_path': '../private.md', 'kind': 'doc',
        'quality': 'reference', 'content_sha256': '0'*64, 'title': 'private'})
    (root/'manifest.json').write_text(json.dumps(manifest))
    task, snapshot = task_input()
    provider = MockProvider([tool(1, 'research.search', {'query': 'synthetic', 'limit': 1}),
        final('no citation', [])])
    result = ResearchAgent(provider, index).run(task, snapshot, Budget())
    assert len(result.observations[0].output['candidates']) <= 1
    assert result.observations[0].output['index_sha256'] == index.content_sha256
    assert result.status == 'failed'
    assert all(item['source_id'] != 'private' for item in result.observations[0].output['candidates'])


def test_research_changed_snapshot_is_rejected_before_tools(tmp_path):
    _, index = copied_index(tmp_path)
    task, snapshot = task_input()
    wrong = replace(task, claim=replace(task.claim, plan_version=2))
    provider = MockProvider([tool(1, 'research.search', {'query': 'duplicate'})])
    result = ResearchAgent(provider, index).run(wrong, snapshot, Budget())
    assert result.error_kind == 'policy_denied' and provider.requests == []


@pytest.mark.parametrize('content', [b'x' * 16385, b'\xff\xfeinvalid-utf8'])
def test_research_source_size_and_encoding_are_bounded(tmp_path, content):
    root = tmp_path/'bounded'
    root.mkdir()
    (root/'source.txt').write_bytes(content)
    spec = SourceSpec('bounded', 'source.txt', 'doc', 'reference', hashlib.sha256(content).hexdigest())
    index = SourceIndex(root, [spec], 'bounded.v1')
    task, snapshot = task_input()
    result = ResearchAgent(MockProvider([tool(1, 'research.read', {'source_id': 'bounded'})]), index).run(task, snapshot, Budget())
    assert result.error_kind == 'policy_denied' and result.observations == ()
