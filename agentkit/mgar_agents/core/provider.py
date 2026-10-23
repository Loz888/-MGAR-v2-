"""Offline model-decision protocol; no network or durable state access."""
from __future__ import annotations

from collections.abc import Callable, Sequence
from dataclasses import dataclass
from typing import Protocol

from .schemas import Observation


@dataclass(frozen=True)
class ModelRequest:
    run_id: str
    task_id: str
    plan_version: int
    turn: int
    logical_call_id: str
    case_id: str
    goal: str
    snapshot_json: str
    observations: tuple[Observation, ...]
    tools_json: str


class Provider(Protocol):
    def decide(self, request: ModelRequest, *, timeout_s: float) -> str: ...


class MockProvider:
    """Explicit fixture decisions; tools always execute outside the Provider."""

    def __init__(self, responses: Sequence[str] | Callable[[ModelRequest], str]) -> None:
        self._responses = tuple(responses) if not callable(responses) else responses
        self.requests: list[ModelRequest] = []

    def decide(self, request: ModelRequest, *, timeout_s: float) -> str:
        self.requests.append(request)
        if callable(self._responses):
            return self._responses(request)
        index = len(self.requests) - 1
        if index >= len(self._responses):
            raise ValueError("mock fixture exhausted")
        return self._responses[index]
