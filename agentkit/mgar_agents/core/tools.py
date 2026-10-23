"""Trusted local tool adapter. This is not an untrusted-code sandbox or P5 gateway."""
from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass, field
import math
import re
import signal
import threading
import time
from types import MappingProxyType
from typing import Any, TypeVar

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from .schemas import MAX_JSON_BYTES, canonical_json, strict_json, validate

T = TypeVar("T")
ROLES = frozenset({"research", "data", "action", "verify", "aggregate"})


class PolicyDenied(PermissionError):
    pass


class ToolExecutionError(Exception):
    pass


def run_bounded(operation: Callable[[], T], timeout_s: float) -> T:
    """Interrupt trusted synchronous handlers on the supported Linux main thread.

    Hostile code, child-process cleanup and other threads require P5 isolation.
    Existing application alarms are preserved by refusing to replace them.
    """
    if threading.current_thread() is not threading.main_thread() or not hasattr(signal, "SIGALRM"):
        raise PolicyDenied("synchronous deadline requires supported main thread")
    previous_timer = signal.getitimer(signal.ITIMER_REAL)
    if previous_timer != (0.0, 0.0):
        raise PolicyDenied("existing application deadline cannot be replaced")
    if not math.isfinite(timeout_s) or timeout_s <= 0:
        raise TimeoutError("operation deadline exhausted")
    previous_handler = signal.getsignal(signal.SIGALRM)
    def expired(signum: int, frame: Any) -> None:
        raise TimeoutError("operation deadline exhausted")
    signal.signal(signal.SIGALRM, expired)
    try:
        signal.setitimer(signal.ITIMER_REAL, timeout_s)
        return operation()
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_handler)


@dataclass(frozen=True)
class ToolSpec:
    name: str
    arguments_schema: dict[str, Any]
    handler: Callable[..., Any] = field(repr=False)
    roles: frozenset[str] = field(default_factory=frozenset)
    scope: str = ""
    effect_class: str = "readonly"
    schema_version: str = "tool.v1"
    timeout_s: float = 2
    max_output_bytes: int = 16384
    _schema_json: str = field(init=False, repr=False)

    def __post_init__(self) -> None:
        if not re.fullmatch(r"[a-z][a-z0-9_.-]{0,63}", self.name):
            raise ValueError("invalid tool name")
        if not self.roles or not set(self.roles) <= ROLES or not self.scope or len(self.scope) > 512:
            raise ValueError("invalid tool authority")
        if self.effect_class not in {"readonly", "idempotent_write", "non_idempotent"} or not self.schema_version:
            raise ValueError("invalid tool declaration")
        if type(self.timeout_s) not in {int, float} or not math.isfinite(self.timeout_s) or not 0 < self.timeout_s <= 300:
            raise ValueError("invalid tool timeout")
        if type(self.max_output_bytes) is not int or not 1 <= self.max_output_bytes <= MAX_JSON_BYTES:
            raise ValueError("invalid tool output bound")
        encoded = canonical_json(self.arguments_schema)
        schema = strict_json(encoded)
        if schema.get("type") != "object" or schema.get("additionalProperties") is not False:
            raise ValueError("tool arguments require a closed object schema")
        def local_references(value: Any) -> None:
            if isinstance(value, dict):
                for key, item in value.items():
                    if key in {"$ref", "$dynamicRef"} and (not isinstance(item, str) or not item.startswith("#")):
                        raise ValueError("external schema references are disabled")
                    local_references(item)
            elif isinstance(value, list):
                for item in value:
                    local_references(item)
        local_references(schema)
        Draft202012Validator.check_schema(schema)
        object.__setattr__(self, "_schema_json", encoded)
        object.__setattr__(self, "roles", frozenset(self.roles))

    def declaration(self) -> dict[str, Any]:
        return {"name": self.name, "schema_version": self.schema_version, "arguments_schema": strict_json(self._schema_json), "scope": self.scope,
                "effect_class": self.effect_class, "timeout_s": self.timeout_s, "max_output_bytes": self.max_output_bytes}


class ToolRegistry:
    def __init__(self, tools: list[ToolSpec] | tuple[ToolSpec, ...]) -> None:
        by_name = {tool.name: tool for tool in tools}
        if len(by_name) != len(tools):
            raise ValueError("duplicate tool declaration")
        self._tools = MappingProxyType(by_name)

    def declarations_json(self, role: str) -> str:
        return canonical_json([tool.declaration() for tool in self._tools.values() if role in tool.roles and tool.effect_class != "non_idempotent"])

    def authorize(self, name: str, arguments: dict[str, Any], role: str) -> ToolSpec:
        spec = self._tools.get(name)
        if spec is None or role not in spec.roles or spec.effect_class == "non_idempotent":
            raise PolicyDenied("tool capability denied")
        try:
            validate(strict_json(spec._schema_json), arguments)
        except (ValueError, ValidationError) as error:
            raise PolicyDenied("invalid tool arguments") from error
        return spec

    def execute(self, name: str, arguments: dict[str, Any], role: str, *, timeout_s: float, max_output_bytes: int) -> str:
        deadline = time.monotonic() + timeout_s
        spec = run_bounded(lambda: self.authorize(name, arguments, role), timeout_s)
        duration = min(spec.timeout_s, deadline - time.monotonic())
        try:
            output = run_bounded(lambda: spec.handler(arguments, timeout_s=duration), duration)
            encoded = canonical_json(output)
        except (PermissionError, TimeoutError):
            raise
        except Exception as error:
            raise ToolExecutionError("trusted tool failed") from error
        if len(encoded.encode("utf-8")) > min(spec.max_output_bytes, max_output_bytes):
            raise PolicyDenied("tool output limit exceeded")
        return encoded
