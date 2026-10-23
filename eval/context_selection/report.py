"""One synthetic fixture, actual selector and Mock Provider request accounting."""
import hashlib
import json
import subprocess

from mgar_agents.core.agent import Agent
from mgar_agents.core.provider import MockProvider
from mgar_agents.core.schemas import Budget, ContextSnapshot
from mgar_agents.core.tools import ToolRegistry
from test_selection import ROOT, fixture_input, select


def main():
    binary = ROOT/'.local/contextselect'
    subprocess.run(['go', 'build', '-o', str(binary), './cmd/contextselect'], cwd=ROOT, check=True)
    task, snapshot, content = fixture_input()
    records = []
    for mode in ['full', 'summary', 'selected']:
        result = select(binary, snapshot, content, mode)
        assert not result['blocked']
        provider = MockProvider(['{"type":"final","output":{}}'])
        run = Agent(provider).run(task, ContextSnapshot(result['wire_json']), ToolRegistry([]), Budget())
        assert run.status == 'completed' and len(provider.requests) == 1
        actual = provider.requests[0].snapshot_json
        assert actual == result['wire_json'] and len(actual.encode()) == result['used_bytes']
        records.append({'mode': mode, 'actual_context_bytes': len(actual.encode()),
            'estimated_tokens': result['estimated_tokens'], 'token_estimator': result['token_estimator'],
            'actual_context_sha256': hashlib.sha256(actual.encode()).hexdigest(),
            'admitted': result['admitted'], 'rejected': result['rejected'],
            'provider_requests': len(provider.requests), 'provider': 'structured_mock', 'command_exit_code': 0})
    output = ROOT/'evidence/batches/P3-03/selection-report.json'
    output.parent.mkdir(parents=True, exist_ok=True)
    report = {'scope': 'One fixed-shape synthetic fixture; accounting only, no model quality or latency claim.',
        'max_context_bytes': 16384, 'max_estimated_tokens': 4096,
        'budget_scope': 'Actual snapshot_json context, including metadata; excludes goal, tools, observations and HTTP envelope.',
        'fixture': {'snapshot': snapshot, 'content_hashes': {key: hashlib.sha256(value.encode()).hexdigest() for key, value in content.items()}},
        'modes': records, 'saved_bytes_selected_vs_full': records[0]['actual_context_bytes']-records[2]['actual_context_bytes'],
        'notes': ['Token estimates are UTF-8 bytes/4 rounded up, not an actual model tokenizer or billing measurement.',
            'CLI fixtures do not authenticate Workers; real PG authorization/summary persistence are independently tested.']}
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
    print(json.dumps({'modes': [{k: row[k] for k in ['mode', 'actual_context_bytes', 'estimated_tokens']} for row in records],
        'saved_bytes_selected_vs_full': report['saved_bytes_selected_vs_full']}))


if __name__ == '__main__':
    main()
