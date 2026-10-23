"""Finite human-reviewed patches in an owned copy of a fixed Go fixture."""
from __future__ import annotations

import difflib
import hashlib
import json
import os
from pathlib import Path
import stat
import tempfile
from types import MappingProxyType
from typing import Any
from uuid import uuid4

from mgar_agents.core.schemas import Task, canonical_json
from mgar_agents.core.tools import PolicyDenied, ToolRegistry, ToolSpec

from .runner import capture, parse_go_events, validate_duration

DEFAULT_FIXTURE = Path(__file__).resolve().parents[3] / 'fixtures/incident/go-repo'
GO = Path('/workspace/.mgar-tools/go/bin/go')
BASE_HASHES = MappingProxyType({
    'aggregate.go': 'fdb55481f7b24ef7b3b3ca8de27ae224fc8639d9403227c490b0c515274f585b',
    'aggregate_test.go': '467f3a45b5785ffed04aa1e4a323ba69dfb974b83832802b4a0b5f18f096edcd',
    'go.mod': '6afc983df3a9d3ae24fa580644b235e0cec3b9cb900ca63a8598fcf95cd4ae16'})
# These are manually authored trusted fixture variants, never model-provided code.
PREFIX = '''package aggregate

type Order struct {
\tID     int
\tStatus string
\tAmount int
}

'''
PATCHES = MappingProxyType({
    'deduplicate-only': (PREFIX + '''func PaidTotal(orders []Order) int {
\ttotal := 0
\tseen := make(map[int]bool)
\tfor _, order := range orders {
\t\tif !seen[order.ID] {
\t\t\ttotal += order.Amount
\t\t\tseen[order.ID] = true
\t\t}
\t}
\treturn total
}
''').encode(),
    'deduplicate-paid': (PREFIX + '''func PaidTotal(orders []Order) int {
\ttotal := 0
\tseen := make(map[int]bool)
\tfor _, order := range orders {
\t\tif order.Status == "paid" && !seen[order.ID] {
\t\t\ttotal += order.Amount
\t\t\tseen[order.ID] = true
\t\t}
\t}
\treturn total
}
''').encode()})


def sha(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def read_inventory(root: Path) -> dict[str, bytes]:
    if root.absolute() != root.resolve() or root.is_symlink():
        raise PolicyDenied('symlink fixture or workspace root denied')
    directory = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        if set(os.listdir(directory)) != set(BASE_HASHES):
            raise PolicyDenied('workspace inventory differs from fixed fixture')
        result = {}
        for name in BASE_HASHES:
            fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
            try:
                info = os.fstat(fd)
                if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > 4096:
                    raise PolicyDenied('fixture must be bounded regular unlinked files')
                content = os.read(fd, 4097)
                if len(content) > 4096:
                    raise PolicyDenied('fixture size exceeded')
                result[name] = content
            finally:
                os.close(fd)
        return result
    except OSError:
        raise PolicyDenied('fixed fixture access denied') from None
    finally:
        os.close(directory)


class ActionWorkspace:
    def __init__(self, task: Task, *, fixture_root: Path = DEFAULT_FIXTURE, audit_dir: Path | None = None,
                 go_timeout_s: float = 30, max_output_bytes: int = 32768) -> None:
        if task.claim.role != 'action':
            raise PolicyDenied('action role required')
        validate_duration(go_timeout_s)
        if type(max_output_bytes) is not int or not 256 <= max_output_bytes <= 32768:
            raise ValueError('bounded action output required')
        content = read_inventory(fixture_root)
        if {name: sha(value) for name, value in content.items()} != dict(BASE_HASHES):
            raise PolicyDenied('trusted fixture hash mismatch')
        self.task = task
        self.go_timeout_s = go_timeout_s
        self.max_output_bytes = max_output_bytes
        self._temporary = tempfile.TemporaryDirectory(prefix='mgar-action-')
        self._base = Path(self._temporary.name)
        self.root = self._base/'repo'
        self.root.mkdir(mode=0o700)
        for name, value in content.items():
            (self.root/name).write_bytes(value)
            (self.root/name).chmod(0o600)
        self._baseline = content['aggregate.go']
        self._hashes = dict(BASE_HASHES)
        self.patch_id = 'baseline'
        self._closed = False
        audit_dir = audit_dir or DEFAULT_FIXTURE.parents[2]/'.local/action-runs'
        audit_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        if audit_dir.absolute() != audit_dir.resolve():
            self.close()
            raise PolicyDenied('audit root must not be a symlink')
        self._audit = Path(tempfile.mkdtemp(prefix='execution-', dir=audit_dir))
        self._environment = {'PATH': str(GO.parent), 'GOTOOLCHAIN': 'local', 'GOPROXY': 'off',
            'GOSUMDB': 'sum.golang.org', 'GOWORK': 'off', 'GOENV': 'off', 'CGO_ENABLED': '0', 'GOMAXPROCS': '2',
            'GOCACHE': str(DEFAULT_FIXTURE.parents[2]/'.local/action-go-cache'),
            'GOPATH': str(self._base/'gopath'), 'GOTMPDIR': str(self._base/'tmp')}
        (self._base/'tmp').mkdir(mode=0o700)

    def __enter__(self) -> ActionWorkspace:
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    def close(self) -> None:
        self._closed = True
        self._temporary.cleanup()

    def _check(self) -> dict[str, bytes]:
        if self._closed:
            raise PolicyDenied('workspace is closed')
        content = read_inventory(self.root)
        if {name: sha(value) for name, value in content.items()} != self._hashes:
            raise PolicyDenied('workspace hash drift denied')
        return content

    @property
    def workspace_sha(self) -> str:
        self._check()
        return sha(canonical_json(self._hashes).encode())

    def inspect(self) -> dict[str, Any]:
        return {'workspace_sha256': self.workspace_sha, 'files': self._hashes.copy(), 'patch_id': self.patch_id,
            'allowed_path': 'aggregate.go', 'patch_catalog': list(PATCHES), 'verified': False}

    def apply_patch(self, arguments: dict[str, Any]) -> dict[str, Any]:
        if (set(arguments) != {'expected_workspace_sha', 'path', 'patch_id', 'expected_file_sha256'}
                or arguments['expected_workspace_sha'] != self.workspace_sha or arguments['path'] != 'aggregate.go'
                or arguments['expected_file_sha256'] != self._hashes['aggregate.go']
                or not isinstance(arguments['patch_id'], str) or arguments['patch_id'] not in PATCHES
                or arguments['patch_id'] == self.patch_id):
            raise PolicyDenied('patch is outside fixed scope or stale')
        replacement = PATCHES[arguments['patch_id']]
        if len(replacement) > 4096:
            raise PolicyDenied('patch size exceeded')
        fd = os.open(self.root/'aggregate.go', os.O_WRONLY | os.O_NOFOLLOW | os.O_TRUNC)
        try:
            os.write(fd, replacement)
        finally:
            os.close(fd)
        self._hashes['aggregate.go'] = sha(replacement)
        self.patch_id = arguments['patch_id']
        return self.inspect()

    def patch_proposal(self) -> dict[str, Any]:
        content = self._check()['aggregate.go']
        return {'patch_id': self.patch_id, 'path': 'aggregate.go', 'before_sha256': BASE_HASHES['aggregate.go'],
            'after_sha256': sha(content), 'diff': ''.join(difflib.unified_diff(self._baseline.decode().splitlines(True),
                content.decode().splitlines(True), fromfile='a/aggregate.go', tofile='b/aggregate.go'))}

    def go_test(self, arguments: dict[str, Any], *, timeout_s: float) -> dict[str, Any]:
        validate_duration(timeout_s)
        if set(arguments) != {'expected_workspace_sha'} or arguments['expected_workspace_sha'] != self.workspace_sha:
            raise PolicyDenied('Go invocation scope or workspace version denied')
        invocation = [str(GO), 'test', '-count=1', '-json', './...']
        source_sha = self.workspace_sha
        records: list[dict[str, Any]] = []
        target = self._audit/(str(uuid4())+'.json')
        def record(raw: bytes, code: int, reason: str | None) -> None:
            output = raw.decode('utf-8', errors='replace')
            parsed = parse_go_events(output, code)
            parsed['passed'] = parsed['passed'] and reason is None
            evidence = {'schema_version': 'go_execution.v1', 'argv': invocation, 'exit_code': code,
                'raw_go_json': output, 'output_sha256': sha(raw), 'stop_reason': reason,
                'workspace_sha256': source_sha, 'files': self._hashes.copy(), 'patch_id': self.patch_id,
                'run_id': self.task.claim.run_id, 'task_id': self.task.claim.task_id, 'case_id': self.task.case_id,
                'plan_version': self.task.claim.plan_version, 'epoch': self.task.claim.epoch,
                'revision': self.task.claim.revision, 'tool_policy_version': 'action_fixture.v1', **parsed}
            with target.open('x') as stream:
                target.chmod(0o600)
                json.dump(evidence, stream, ensure_ascii=False, indent=2)
                stream.write('\n')
            records.append(evidence)
        capture(invocation, self.root, self._environment,
            min(timeout_s, self.go_timeout_s), self.max_output_bytes, on_exit=record)
        self._check()
        return {key: value for key, value in records[0].items() if key != 'raw_go_json'} | {
            'execution_log': str(target), 'verified': False}

    def tools(self) -> ToolRegistry:
        digest = {'type': 'string', 'pattern': '^[0-9a-f]{64}$'}
        patch_schema = {'type': 'object', 'additionalProperties': False,
            'required': ['expected_workspace_sha', 'path', 'patch_id', 'expected_file_sha256'],
            'properties': {'expected_workspace_sha': digest, 'expected_file_sha256': digest,
                'path': {'const': 'aggregate.go'}, 'patch_id': {'enum': list(PATCHES)}}}
        test_schema = {'type': 'object', 'additionalProperties': False, 'required': ['expected_workspace_sha'],
            'properties': {'expected_workspace_sha': digest}}
        def inspect(arguments: dict[str, Any], *, timeout_s: float) -> dict[str, Any]:
            return self.inspect()
        def patch(arguments: dict[str, Any], *, timeout_s: float) -> dict[str, Any]:
            return self.apply_patch(arguments)
        return ToolRegistry([
            ToolSpec('action.inspect', {'type': 'object', 'additionalProperties': False}, inspect,
                frozenset({'action'}), scope='trusted_incident_fixture'),
            ToolSpec('action.apply_patch', patch_schema, patch, frozenset({'action'}),
                scope='trusted_incident_fixture/aggregate.go', effect_class='idempotent_write'),
            ToolSpec('action.go_test', test_schema, self.go_test, frozenset({'action'}),
                scope='trusted_incident_fixture', timeout_s=self.go_timeout_s)])
