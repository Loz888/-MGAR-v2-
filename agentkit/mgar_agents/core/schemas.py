"""Versioned SDK values. SDK results never authorize Workflow state changes."""
from __future__ import annotations

from dataclasses import asdict, dataclass, field
import hashlib
import json
import math
from pathlib import Path
from typing import Any
from uuid import UUID

from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry

from .claim import Claim

CONTRACTS = Path(__file__).resolve().parents[3] / "contracts"
MAX_JSON_BYTES = 65536


def canonical_json(value: Any) -> str:
    def bounded(item: Any, depth: int = 0) -> None:
        if depth > 32:
            raise ValueError("JSON nesting limit")
        if item is None or type(item) in {str, bool, int}:
            return
        if type(item) is float and math.isfinite(item):
            return
        if type(item) is list:
            for child in item:
                bounded(child, depth + 1)
            return
        if type(item) is dict and all(type(k) is str for k in item):
            for child in item.values():
                bounded(child, depth + 1)
            return
        raise ValueError("not finite JSON data")
    bounded(value)
    encoded = json.dumps(value, ensure_ascii=False, allow_nan=False, sort_keys=True, separators=(",", ":"))
    if len(encoded.encode("utf-8")) > MAX_JSON_BYTES:
        raise ValueError("JSON byte limit")
    return encoded


def strict_json(text: str) -> dict[str, Any]:
    if type(text) is not str or len(text.encode("utf-8")) > MAX_JSON_BYTES:
        raise ValueError("invalid JSON input")
    def pairs(items: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in items:
            if key in result:
                raise ValueError("duplicate JSON property")
            result[key] = value
        return result
    def constant(value: str) -> None:
        raise ValueError("non-finite JSON value")
    value: Any = json.loads(text, object_pairs_hook=pairs, parse_constant=constant)
    canonical_json(value)
    if type(value) is not dict:
        raise ValueError("expected JSON object")
    return value


def validate(schema: dict[str, Any], value: Any) -> None:
    canonical_json(value)
    # Validation must never turn a schema URI into implicit network/file I/O.
    # A fresh referencing.Registry defaults to refusing retrieval; jsonschema's
    # implicit default registry may otherwise retrieve remote references.
    Draft202012Validator(schema, format_checker=FormatChecker(), registry=Registry()).validate(value)


@dataclass(frozen=True)
class Task:
    claim: Claim
    goal: str
    case_id: str = "offline"

    def __post_init__(self) -> None:
        claim = self.claim
        if not isinstance(claim, Claim):
            raise ValueError("invalid task claim")
        if any(type(value) is not int for value in (claim.epoch, claim.revision, claim.plan_version)) or claim.epoch < 1 or claim.revision < 0 or claim.plan_version < 1:
            raise ValueError("invalid task fencing version")
        if type(claim.owner) is not str or not claim.owner or len(claim.owner) > 128 or claim.role not in {"research", "data", "action", "verify", "aggregate"}:
            raise ValueError("invalid task identity")
        for identity in (claim.run_id, claim.task_id):
            if type(identity) is not str:
                raise ValueError("invalid task UUID")
            try:
                if str(UUID(identity)) != identity.lower():
                    raise ValueError("task UUID must use schema-compatible spelling")
            except ValueError as error:
                raise ValueError("invalid task UUID") from error
        if type(self.goal) is not str or not self.goal.strip() or len(self.goal) > 4096:
            raise ValueError("invalid task goal")
        if type(self.case_id) is not str or not self.case_id or len(self.case_id) > 128:
            raise ValueError("invalid case identity")


@dataclass(frozen=True)
class ContextSnapshot:
    _json: str

    def __post_init__(self) -> None:
        raw = strict_json(self._json)
        validate(json.loads((CONTRACTS / "context_snapshot.schema.json").read_text()), raw)
        selection = raw.get('context_selection')
        if selection is not None:
            base = {key: value for key, value in raw.items() if key != 'context_selection'}
            if hashlib.sha256(canonical_json(base).encode()).hexdigest() != selection['source_snapshot_sha256']:
                raise ValueError('selected context has a different source snapshot')
            refs = {ref['artifact_id']: ref for ref in raw['artifacts']}
            seen: set[str] = set()
            for item in selection['items']:
                ref = refs.get(item['artifact_id'])
                if ref is None or item['artifact_id'] in seen or ref['content_sha256'] != item['content_sha256']:
                    raise ValueError('selected context is outside the fixed references')
                seen.add(item['artifact_id'])
                if item['original']:
                    body = item['text'].encode()
                    if hashlib.sha256(body).hexdigest() != item['content_sha256'] or len(body) != ref.get('byte_length'):
                        raise ValueError('selected original differs from source content')
        object.__setattr__(self, "_json", canonical_json(raw))

    @classmethod
    def from_dict(cls, value: dict[str, Any]) -> ContextSnapshot:
        return cls(canonical_json(value))

    def to_dict(self) -> dict[str, Any]:
        return strict_json(self._json)

    @property
    def content_sha256(self) -> str:
        return hashlib.sha256(self._json.encode()).hexdigest()


@dataclass(frozen=True)
class Budget:
    max_turns: int = 8
    max_tool_calls: int = 6
    timeout_s: float = 10
    max_observation_bytes: int = 16384

    def __post_init__(self) -> None:
        if type(self.max_turns) is not int or not 1 <= self.max_turns <= 64:
            raise ValueError("invalid turn limit")
        if type(self.max_tool_calls) is not int or not 0 <= self.max_tool_calls <= 64:
            raise ValueError("invalid tool-call limit")
        if type(self.timeout_s) not in {int, float} or not math.isfinite(self.timeout_s) or not 0 < self.timeout_s <= 300:
            raise ValueError("invalid timeout")
        if type(self.max_observation_bytes) is not int or not 1 <= self.max_observation_bytes <= MAX_JSON_BYTES:
            raise ValueError("invalid observation byte limit")


@dataclass(frozen=True)
class Observation:
    turn: int
    call_id: str
    name: str
    status: str
    output_json: str
    output_sha256: str

    @property
    def output(self) -> Any:
        return json.loads(self.output_json)

    def to_dict(self) -> dict[str, Any]:
        return {"turn": self.turn, "call_id": self.call_id, "name": self.name, "status": self.status, "output": self.output, "output_sha256": self.output_sha256}


@dataclass(frozen=True)
class Event:
    sequence: int
    kind: str
    turn: int
    tool_name: str | None = None
    call_id: str | None = None
    observation_hash: str | None = None
    error_kind: str | None = None


@dataclass(frozen=True)
class AgentResult:
    task: Task
    status: str
    turns: int
    tool_calls: int
    output_json: str | None
    error_kind: str | None
    error_turn: int | None
    error_tool: str | None
    observations: tuple[Observation, ...]
    events: tuple[Event, ...]
    verified: bool = field(default=False, init=False)

    @property
    def output(self) -> Any:
        return None if self.output_json is None else json.loads(self.output_json)

    def to_dict(self) -> dict[str, Any]:
        claim = self.task.claim
        return {"schema_version": "agent_result.v1", "run_id": claim.run_id, "trace_id": claim.run_id,
                "task_id": claim.task_id, "plan_version": claim.plan_version, "owner": claim.owner,
                "epoch": claim.epoch, "revision": claim.revision, "case_id": self.task.case_id,
                "status": self.status, "turns": self.turns, "tool_calls": self.tool_calls,
                "verified": False, "output": self.output, "error_kind": self.error_kind,
                "error_turn": self.error_turn, "error_tool": self.error_tool,
                "observations": [o.to_dict() for o in self.observations], "events": [asdict(e) for e in self.events]}
