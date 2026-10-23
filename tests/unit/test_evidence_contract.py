"""Executable schema checks for the verifier's complete wire evidence."""
import copy
import json
from pathlib import Path

import jsonschema
import pytest

ROOT = Path(__file__).resolve().parents[2]
SCHEMA = json.loads((ROOT / 'contracts/evidence.schema.json').read_text())
VALIDATOR = jsonschema.Draft202012Validator(SCHEMA, format_checker=jsonschema.FormatChecker())
EVIDENCE = {
    'schema_version': 'evidence.v1', 'claim': 'pinned content hash matches', 'kind': 'file_hash',
    'artifact_id': 'b76b4412-397b-4c51-9ba8-6b50f7d2f271', 'content_sha256': 'a' * 64,
    'producer_run_id': 'b76b4412-397b-4c51-9ba8-6b50f7d2f272',
    'producer_task_id': 'b76b4412-397b-4c51-9ba8-6b50f7d2f273', 'source_plan_version': 1,
    'input_snapshot_id': 'b76b4412-397b-4c51-9ba8-6b50f7d2f274', 'tool_version': 'fixture.v1',
    'execution_status': 'succeeded', 'replay_recipe': {'method': 'cas_hash'},
    'observed': {'sha256': 'a' * 64}, 'expected_observation': {'sha256': 'a' * 64},
}


def test_evidence_contract_valid():
    VALIDATOR.check_schema(SCHEMA)
    VALIDATOR.validate(EVIDENCE)


@pytest.mark.parametrize('field', SCHEMA['required'])
def test_evidence_contract_missing(field):
    value = copy.deepcopy(EVIDENCE)
    del value[field]
    assert list(VALIDATOR.iter_errors(value))


@pytest.mark.parametrize('mutation', ['reviewer', 'shell', 'uuid', 'observation'])
def test_evidence_contract_rejects_injection(mutation):
    value = copy.deepcopy(EVIDENCE)
    if mutation == 'reviewer':
        value['reviewer_passed'] = True
    elif mutation == 'shell':
        value['replay_recipe']['shell'] = 'untrusted text'
    elif mutation == 'uuid':
        value['producer_task_id'] = 'not-a-task'
    else:
        value['observed']['rows'] = [[{'forged': True}]]
    assert list(VALIDATOR.iter_errors(value))
