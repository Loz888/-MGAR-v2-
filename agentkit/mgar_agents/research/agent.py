"""Research role over real synthetic sources; findings remain unverified."""
from __future__ import annotations

from dataclasses import replace
import hashlib
from typing import Any
from uuid import NAMESPACE_URL, uuid5

from jsonschema.exceptions import ValidationError

from mgar_agents.core.agent import Agent
from mgar_agents.core.provider import Provider
from mgar_agents.core.schemas import AgentResult, Budget, ContextSnapshot, Event, Task, canonical_json, validate

from .index import ID_PATTERN, SourceIndex

FINAL_SCHEMA: dict[str, Any] = {
    "type": "object", "additionalProperties": False, "required": ["statement", "citations"],
    "properties": {"statement": {"type": "string", "minLength": 1, "maxLength": 4096, "pattern": r"\S"},
        "citations": {"type": "array", "minItems": 1, "maxItems": 8, "items": {"type": "object", "additionalProperties": False,
            "required": ["source_id", "content_sha256", "quote"], "properties": {
                "source_id": {"type": "string", "pattern": ID_PATTERN},
                "content_sha256": {"type": "string", "pattern": "^[0-9a-f]{64}$"},
                "quote": {"type": "string", "minLength": 1, "maxLength": 1024, "pattern": r"\S"}}}}},
}


class ResearchAgent:
    def __init__(self, provider: Provider, index: SourceIndex) -> None:
        self.provider = provider
        self.index = index

    def run(self, task: Task, snapshot: ContextSnapshot, budget: Budget) -> AgentResult:
        if task.claim.role != "research":
            return AgentResult(task, "failed", 0, 0, None, "policy_denied", 0, None, (),
                (Event(1, "run_started", 0), Event(2, "research_finding_rejected", 0, error_kind="policy_denied"),
                 Event(3, "run_failed", 0, error_kind="policy_denied")))
        result = Agent(self.provider).run(task, snapshot, self.index.tools(), budget)
        if result.status != "completed":
            return result
        try:
            proposed = result.output
            validate(FINAL_SCHEMA, proposed)
            observed = {item.output["source_id"]: item.output for item in result.observations if item.name == "research.read"}
            citations: list[dict[str, Any]] = []
            seen: set[tuple[str, str]] = set()
            for citation in proposed["citations"]:
                source = observed.get(citation["source_id"])
                if source is None or citation["content_sha256"] != source["content_sha256"] or citation["quote"] not in source["text"]:
                    raise ValueError("citation does not correspond to an actual source read")
                identity = (citation["source_id"], citation["quote"])
                if identity in seen:
                    raise ValueError("duplicate citation")
                seen.add(identity)
                candidate_id = str(uuid5(NAMESPACE_URL, canonical_json({"run": task.claim.run_id, "task": task.claim.task_id,
                    "source": citation["source_id"], "hash": source["content_sha256"], "index": self.index.content_sha256})))
                citations.append({**citation, "artifact_uri": "artifact://" + candidate_id, "persisted": False, "verified": False,
                    "quality": source["quality"], "kind": source["kind"], "byte_length": source["byte_length"],
                    "index_version": source["index_version"], "index_sha256": source["index_sha256"],
                    "producer_run_id": task.claim.run_id, "producer_task_id": task.claim.task_id,
                    "source_plan_version": task.claim.plan_version})
            output = canonical_json({"schema_version": "finding.v1", "statement": proposed["statement"], "citations": citations,
                "verified": False, "source_index_sha256": self.index.content_sha256, "input_snapshot_sha256": snapshot.content_sha256,
                "producer_run_id": task.claim.run_id, "producer_task_id": task.claim.task_id, "source_plan_version": task.claim.plan_version})
        except (ValueError, ValidationError, KeyError, TypeError):
            event = Event(len(result.events) + 1, "research_finding_rejected", result.turns, error_kind="evidence_rejected")
            return replace(result, status="failed", output_json=None, error_kind="evidence_rejected", error_turn=result.turns,
                error_tool=None, events=result.events + (event, Event(len(result.events) + 2, "run_failed", result.turns, error_kind="evidence_rejected")))
        event = Event(len(result.events) + 1, "research_finding_prepared", result.turns,
            observation_hash=hashlib.sha256(output.encode()).hexdigest())
        return replace(result, output_json=output, events=result.events + (event,))
