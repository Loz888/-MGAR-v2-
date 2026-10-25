"""Offline preparation only: no credentials read and no network operations."""
import hashlib
import json
from collections import Counter
from pathlib import Path

root = Path(__file__).resolve().parents[3]
manifest = root / 'eval/cases/frozen-live-24.json'
sha = hashlib.sha256(manifest.read_bytes()).hexdigest()
assert sha == 'ec6d5576a75fc2724756abeef2628189c2922038f09cb9258d7a1972e493c8ab'
cases = json.loads(manifest.read_text())
assert len(cases) == len({c['case_id'] for c in cases}) == 24
assert Counter(c['difficulty'] for c in cases) == {'simple': 8, 'medium': 8, 'hard': 8}
assert Counter(c['business'] for c in cases) == {'incident': 12, 'analysis': 12}
assert sum(c['negative'] for c in cases) == 6
arms = ['B0', 'B1', 'B2', 'B4']
plan = {
    'schema_version': 'live-experiment-proposal.v1',
    'status': 'PROPOSED_NOT_AUTHORIZED',
    'card_id': 'P7-06',
    'manifest': 'eval/cases/frozen-live-24.json', 'manifest_sha256': sha,
    'dataset_manifest_sha256': '0b48a86008574d0a8bdbde1d88d37e718d45e75fe33363075fdcd48f0eb2d511',
    'arms': arms, 'assignments': [{'case_id': c['case_id'], 'arm': arm, 'status': 'NOT_RUN'} for c in cases for arm in arms],
    'deferred_arms': [{'arm': arm, 'status': 'NOT_RUN', 'reason': 'outside initial four-arm proposal'} for arm in ['B3', 'A1', 'A2']],
    'model_selection': None, 'candidate_provider': 'deepseek', 'candidate_model': 'deepseek-chat',
    'candidate_endpoint': 'https://api.deepseek.com/chat/completions',
    'candidate_key_variable': 'DEEPSEEK_API_KEY',
    'pricing_version': None, 'input_usd_per_million': None, 'output_usd_per_million': None,
    'approved_max_cost_usd': None, 'estimated_cost_usd': None,
    'required_runtime_gates': ['--confirm-live', 'MGAR_ALLOW_PAID=1', 'MGAR_MAX_COST_USD', 'MGAR_LIVE_CASELIST', 'secure provider key', 'frozen verified pricing', 'durable real-provider integration'],
    'request_count_ceiling': len(arms) * sum(c['limits']['model_calls'] for c in cases),
    'token_budget_ceiling': len(arms) * sum(c['limits']['tokens'] for c in cases),
    'frozen_case_total_cost_ceiling_usd': len(arms) * sum(c['limits']['cost_micros'] for c in cases) / 1_000_000,
    'budget_allocation_proposal': 'Immutable 96 slots; each cap <= min(frozen case cap, floor(user cap in micro-USD / 96)); no redistribution or automatic rerun of unknown slots.',
    'bounds_note': 'Ceilings are schema bounds, not expected consumption or approved spend. No price or user cap has been supplied.',
    'runtime_integration_status': 'NOT_IMPLEMENTED', 'paid_calls': 0,
}
assert len(plan['assignments']) == 96
for path, data in [(root / 'eval/live/plan.json', plan), (root / 'evidence/eval/live/assignment-proposal.json', plan)]:
    with path.open('x') as stream:
        json.dump(data, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
print(json.dumps({'status': 'PREPARATION_ONLY', 'cases': len(cases), 'assignments': 96, 'model_requests': 0,
                  'manifest_sha256': sha, 'request_ceiling': plan['request_count_ceiling'], 'prices_and_budget': 'NOT_CONFIGURED'}))
