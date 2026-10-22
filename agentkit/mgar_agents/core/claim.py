"""Client-side identity guard; server-side Go fencing remains authoritative."""
from dataclasses import dataclass


@dataclass(frozen=True)
class Claim:
    task_id: str
    run_id: str
    owner: str
    role: str
    epoch: int
    revision: int
    plan_version: int

    def __post_init__(self) -> None:
        if not self.owner or not self.task_id or not self.run_id:
            raise ValueError("missing identity")
        if self.role not in {"research", "data", "action", "verify", "aggregate"}:
            raise ValueError("unknown role")
        if type(self.epoch) is not int or self.epoch < 1 or self.revision < 0 or self.plan_version < 1:
            raise ValueError("invalid fencing version")

    def require_identity(self, owner: str, role: str) -> None:
        if owner != self.owner or role != self.role:
            raise PermissionError("claim identity mismatch; stop writes, do not cancel workflow")
