"""Review the pinned public codec and freeze its conservative text-only bound."""
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import sys
import zipfile

root = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(root))
from eval.live.tokenizer_data import OFFICIAL_TEXT_BOUND, audit_archive, proof_fingerprint

source = root / 'internal/provider/data/deepseek_v4_tokenizer.zip'
raw = source.read_bytes()
audit = audit_archive(raw)
proof = OFFICIAL_TEXT_BOUND.copy()
assert audit['archive_sha256'] == proof['archive_sha256']
with zipfile.ZipFile(source) as archive:
    codec_raw = archive.read('deepseek_v4_tokenizer/tokenizer.json')
    config_raw = archive.read('deepseek_v4_tokenizer/tokenizer_config.json')
codec, config = json.loads(codec_raw), json.loads(config_raw)
assert hashlib.sha256(codec_raw).hexdigest() == proof['tokenizer_sha256']
assert hashlib.sha256(config_raw).hexdigest() == proof['config_sha256']
assert hashlib.sha256(config['chat_template'].encode()).hexdigest() == proof['template_sha256']
assert codec['normalizer'] == {'type': 'Sequence', 'normalizers': []}
assert codec['pre_tokenizer']['type'] == 'Sequence'
steps = codec['pre_tokenizer']['pretokenizers']
assert len(steps) == 4 and all(s['type'] == 'Split' and s['behavior'] == 'Isolated' and not s['invert'] for s in steps[:3])
assert steps[3] == {'type': 'ByteLevel', 'add_prefix_space': False, 'trim_offsets': True, 'use_regex': False}
assert codec['post_processor']['type'] == 'ByteLevel'
model = codec['model']
assert model['type'] == 'BPE' and model['unk_token'] is None and model['continuing_subword_prefix'] is None and model['end_of_word_suffix'] is None
base = [b for b in range(256) if 33 <= b <= 126 or 161 <= b <= 172 or b >= 174]
missing = [b for b in range(256) if b not in base]
mapping = {b: chr(b) for b in base} | {b: chr(256 + i) for i, b in enumerate(missing)}
assert len(mapping) == 256 and all(symbol in model['vocab'] for symbol in mapping.values())
assert all(token['content'] for token in codec['added_tokens'])
provider_source = (root / 'internal/provider/input_bound.go').read_text()
system = re.search(r'const decisionSystem = `([^`]+)`', provider_source).group(1)
wrapper = config['bos_token']['content'] + '<｜User｜><｜Assistant｜>'
maximum = 48000 + len((wrapper + system).encode())
cost_bound = (maximum * 300000 + 1024 * 1200000 + 999999) // 1000000
assert maximum + 1024 <= 65536 and cost_bound <= 20833
certificate = {'certificate': proof, 'proof_sha256': proof_fingerprint(proof), 'archive_audit': audit,
               'source_url': 'https://cdn.deepseek.com/api-docs/deepseek_v4_tokenizer.zip',
               'source_doc': 'https://api-docs.deepseek.com/quick_start/token_usage/',
               'base_byte_symbols_verified': len(mapping), 'vocab_entries': len(model['vocab']),
               'system_instruction_sha256': hashlib.sha256(system.encode()).hexdigest(),
               'system_and_wrapper_bytes': len((wrapper + system).encode()),
               'maximum_request_input_tokens': maximum, 'maximum_request_cost_micros': cost_bound,
               'request_scope': ['one system and one user text message', 'non-thinking',
                                 'no native tools, names, images, prefix completion or response_format'],
               'derivation': ['Identity normalization does not expand UTF-8.',
                              'Isolated splits preserve all content; ByteLevel has no added prefix space.',
                              'Each input byte has an initial vocabulary symbol; BPE merges cannot increase count.',
                              'Added-token matches replace nonempty content with one ID; ByteLevel postprocessing changes offsets only.',
                              'Published template on the closed request branch is BOS + system + User + user + Assistant.',
                              'Reserve every UTF-8 byte of that exact reference string, including literal special markers.'],
               'legacy_model_max_length_metadata': config['model_max_length'],
               'limitations': ['Reference serializer contract is limited to the pinned official text-only mode; other server features are rejected.',
                               'Offline model_max_length metadata is not used to infer current API capacity.',
                               'A reported usage exceeding the bound invalidates the contract and retains unknown reservation, stopping subsequent assignments.',
                               'No remote Python code or native tokenizer plugin was executed.']}
out = root / 'evidence/batches/P7-06D'
(out / 'reference-proof.json').write_text(json.dumps(certificate, ensure_ascii=False, indent=2) + '\n')
evidence_path = root / 'eval/live/official-evidence.json'
evidence = json.loads(evidence_path.read_text())
evidence['input_bound'] = {k: v for k, v in certificate.items() if k != 'archive_audit'}
evidence['generation_parameters']['response_format'] = 'omitted: strict JSON is validated by the controller'
evidence['generation_parameters']['stream'] = False
evidence['limitations'] = [x for x in evidence['limitations'] if not x.startswith('Full-context reservation')]
evidence['limitations'].append('The text-only input bound uses the pinned official reference serializer/codec contract; unknown usage is not zero-filled.')
wire = (json.dumps(evidence, ensure_ascii=False, indent=2) + '\n').encode()
evidence_path.write_bytes(wire)
path = root / 'eval/live/pricing.json'
price = json.loads(path.read_text())
price['config']['input_bound'] = proof
price['config']['max_prompt_bytes'] = 48000
price['config']['official_evidence_sha256'] = hashlib.sha256(wire).hexdigest()
base_version = price['config']['price']['version'].split('.u8-')[0]
price['config']['price']['version'] = base_version + '.u8-' + proof_fingerprint(proof)[:16]
path.write_text(json.dumps(price, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'data_audit_and_reference_proof': 'PASS', 'proof_sha256': proof_fingerprint(proof),
                  'maximum_request_input_tokens': maximum, 'maximum_request_cost_micros': cost_bound,
                  'output_token_cap': 1024, 'global_cap_micros': 2000000, 'assignments': 96,
                  'timestamp_utc': datetime.now(timezone.utc).isoformat(), 'generation_requests_sent': 0}))
