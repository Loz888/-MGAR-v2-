"""Freeze observed public prices; never authorizes or sends a model request."""
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path

root = Path(__file__).resolve().parents[3]
base = root / 'evidence/batches/P7-06C/official-check-20261026'
fetched = json.loads((base / 'fetch.json').read_text())
pricing = (base / 'pricing-slash.txt').read_text()
assert all(term in pricing for term in ['DeepSeek-V4.1-Flash-1017', 'deepseek-flash', 'CONTEXT LENGTH\n1M',
                                        'CACHE MISS', 'PEAK\n$0.3', 'PEAK\n$1.2'])
assert fetched['models']['http_status'] == 200 and 'deepseek-flash' in fetched['models']['model_ids']
sources = []
for name in ['pricing-slash', 'news261017']:
    observed = fetched[name]
    path = base / observed['artifact']
    assert hashlib.sha256(path.read_bytes()).hexdigest() == observed['sha256']
    sources.append({'url': observed['final_url'], 'http_status': 200, 'path': str(path.relative_to(root)),
                    'sha256': observed['sha256'], 'retrieved_at': observed['time_utc']})
reviewed = datetime.now(timezone.utc).isoformat(timespec='seconds')
evidence = {'schema_version': 'official-provider-evidence.v1', 'reviewed_at': reviewed,
            'logical_model': 'flash-1017', 'api_model': 'deepseek-flash',
            'served_version_at_review': 'DeepSeek-V4.1-Flash-1017', 'alias_is_mutable': True,
            'model_list_observation': fetched['models'], 'sources': sources,
            'price_basis': {'currency': 'USD', 'unit_tokens': 1000000, 'input': 'peak cache-miss ceiling',
                            'output': 'peak ceiling', 'input_micros_per_million': 300000,
                            'output_micros_per_million': 1200000},
            'context': {'published_label': '1M', 'conservative_interpretation_tokens': 1048576,
                        'interpretation': 'API documentation defines K as 1024 (384K = 393216); use 1024 squared for 1M. Even 1000000 cannot fit the current frozen share.'},
            'generation_parameters': {'thinking': {'type': 'disabled'}, 'temperature': 0, 'max_tokens': 1024},
            'generation_requests_sent': 0,
            'limitations': ['Official API alias is mutable; no immutable model weights or future version guarantee.',
                            'Peak cache-miss rates are conservative estimates, not an actual provider invoice.',
                            'Full-context reservation does not fit frozen cost/token limits; this evidence does not enable live generation.']}
wire = (json.dumps(evidence, ensure_ascii=False, indent=2) + '\n').encode()
(root / 'eval/live/official-evidence.json').write_bytes(wire)
price = {'status': 'VERIFIED', 'config': {
    'price': {'provider': 'deepseek', 'model': 'flash-1017', 'version': 'deepseek.flash1017.20261026.peak-miss.v1',
              'input_micros_per_million': 300000, 'output_micros_per_million': 1200000},
    'context_tokens': 1048576, 'output_tokens': 1024, 'max_prompt_bytes': 65536,
    'official_evidence_sha256': hashlib.sha256(wire).hexdigest()},
    'pricing_url': 'https://api-docs.deepseek.com/quick_start/pricing',
    'models_url': 'https://api.deepseek.com/models', 'reviewed_at': reviewed}
(root / 'eval/live/pricing.json').write_text(json.dumps(price, indent=2) + '\n')
bound = lambda inp: (inp * 300000 + 1024 * 1200000 + 999999) // 1000000
assert bound(1048576) == 315802 and bound(1000000) == 301229
print(json.dumps({'official_price': 'VERIFIED', 'api_model': 'deepseek-flash',
                  'conservative_call_bound_micros': bound(1048576), 'assignment_cap_micros': 20833,
                  'required_input_output_reservation_tokens': 1048576 + 1024, 'assignment_token_limit': 65536,
                  'generation_requests_sent': 0}))
