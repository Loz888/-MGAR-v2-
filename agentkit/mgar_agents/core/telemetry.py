"""W3C correlation and closed GenAI attributes; never an authority credential."""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
import re

SCHEMA_URL = "https://opentelemetry.io/schemas/1.37.0"
_UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
_PARENT = re.compile(r"00-([0-9a-f]{32})-([0-9a-f]{16})-(0[01])")


@dataclass(frozen=True)
class TraceContext:
    traceparent: str

    def __post_init__(self) -> None:
        match = _PARENT.fullmatch(self.traceparent)
        if not match or int(match[1], 16) == 0 or int(match[2], 16) == 0:
            raise ValueError("INVALID_TRACE_CONTEXT")

    @classmethod
    def for_agent(cls, run_id: str, task_id: str) -> TraceContext:
        if not _UUID.fullmatch(run_id) or not _UUID.fullmatch(task_id):
            raise ValueError("INVALID_TRACE_ID")
        span = hashlib.sha256(f"{run_id}\0agent\0{task_id}".encode()).hexdigest()[:16]
        return cls(f"00-{run_id.replace('-', '')}-{span}-01")

    def headers(self) -> dict[str, str]:
        return {"traceparent": self.traceparent}


def safe_attributes(operation: str, role: str, *, known: bool = False,
                    input_tokens: int | None = None, output_tokens: int | None = None) -> dict[str, str | bool | int]:
    if operation not in {"invoke_agent", "chat", "execute_tool"} or role not in {"research", "data", "action", "verify", "aggregate"}:
        raise ValueError("INVALID_TELEMETRY_ENUM")
    if type(known) is not bool:
        raise ValueError("INVALID_USAGE_FLAG")
    out: dict[str, str | bool | int] = {"gen_ai.operation.name": operation, "mgar.agent.role": role, "mgar.usage.known": known}
    for name, value in [("input", input_tokens), ("output", output_tokens)]:
        if value is not None:
            if not known or type(value) is not int or not 0 <= value <= 10_000_000:
                raise ValueError("INVALID_USAGE")
            out[f"gen_ai.usage.{name}_tokens"] = value
    return out
