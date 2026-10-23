#!/usr/bin/env python3
"""P0-01 static design checks; stdlib only, with no runtime or network actions.

This validates a bounded document contract, not arbitrary natural-language truth,
JSON Schema instances, SQL concurrency, sandbox isolation, or agent capability.
"""

import argparse
import json
from pathlib import Path
import re


VERSIONS = {
    "docs/architecture.md": "architecture.v1",
    "docs/protocols.md": "protocols.v1",
    "docs/adr/ADR-0001.md": "adr.v1",
    "contracts/README.md": "contracts-index.v1",
}
SOURCES = ("01_PROJECT_CHARTER.md", "02_ARCHITECTURE_CONTRACTS.md")
TERMS = {
    "WorkflowRun", "PlanVersion", "NodeKey", "TaskInstance", "Worker",
    "AgentRole", "Epoch", "Lease", "Revision", "Artifact", "ContextSnapshot",
    "Evidence", "Verdict", "LogicalCall", "PhysicalCall", "MockProvider",
    "ExecutionAdapter",
}
TRUST = {
    "GoControlPlane": ("服务端", "预算", "身份"),
    "PythonWorker": ("API", "不得"),
    "LLM": ("不能", "授权"),
    "DBReader": ("独立", "只读", "合成"),
    "Sandbox": ("无控制面/模型密钥", "禁网", "非 root"),
    "Verifier": ("权限分离", "重执行"),
    "Aggregator": ("来源", "验证", "未知"),
    "EventLedger": ("审计权威", "不能替代"),
}
WORKFLOW = {
    ("queued", "planning"), ("planning", "running"),
    ("running", "verifying"), ("verifying", "succeeded"),
    ("verifying", "failed"), ("verifying", "cancelled"),
    ("succeeded", "∅"), ("failed", "∅"), ("cancelled", "∅"),
}
TASK = {
    ("pending", "ready"), ("ready", "claimed"), ("claimed", "executing"),
    ("executing", "output_ready"), ("output_ready", "verifying"),
    ("verifying", "succeeded"), ("verifying", "failed"),
    ("succeeded", "∅"), ("failed", "∅"), ("cancelled", "∅"),
} | {
    (state, "cancelled") for state in (
        "pending", "ready", "claimed", "executing", "output_ready",
        "verifying", "blocked",
    )
}
LOCK_ORDER = (
    "workflow_runs → orchestrator_leases → run_budgets → "
    "task_instances → outbox_events"
)


def section(text, heading):
    """Get one level-2 Markdown section, without including later sections."""
    marker = heading + "\n"
    if marker not in text:
        return ""
    return text.split(marker, 1)[1].split("\n## ", 1)[0]


def table(text):
    """Read the deliberately simple tables used by this design contract."""
    rows = []
    for line in text.splitlines():
        if not line.startswith("|"):
            continue
        cells = [cell.strip().replace("`", "") for cell in line.strip("|").split("|")]
        if all(re.fullmatch(r":?-+:?", cell) for cell in cells):
            continue
        rows.append(cells)
    return rows[1:]  # first row is the header


def transitions(text):
    edges = []
    for row in table(text):
        if len(row) != 3 or not row[2].strip():
            return []
        edges.extend((start, row[1]) for start in row[0].split(","))
    return edges


def claim_violations(documents):
    """Catch documented dangerous assertions, scoped to individual clauses.

    A disclaimer in another sentence or before 'but' must not whitelist a claim.
    Finite lexical rules intentionally do not claim general language inference.
    """
    violations = []
    negatives = re.compile(
        r"不承诺|不保证|不能|不得|不等于|不代表|无法|未实现|未验证|"
        r"分别统计|分开统计|\b(?:no|not|never|cannot|can't)\b", re.I,
    )
    for name, text in documents.items():
        for line_number, line in enumerate(text.splitlines(), 1):
            clauses = re.split(r"[。！？.!?；;\n]|\bbut\b|然而|但是|但", line, flags=re.I)
            for clause in clauses:
                mock = re.search(r"\bmock(?:provider)?\b", clause, re.I)
                real = re.search(r"真实\s*(?:Agent|模型)|real\s+(?:agent|model)", clause, re.I)
                host = re.search(r"跨主机容错|cross[- ]host\s+fault\s+tolerance", clause, re.I)
                external = re.search(r"外部|\bexternal\b", clause, re.I)
                exactly = re.search(r"exactly[- ]once|恰好一次", clause, re.I)
                if (mock and real or host or external and exactly) and not negatives.search(clause):
                    violations.append(f"{name}:{line_number}: {clause.strip()}")
    return violations


def validate(root):
    checks = []

    def check(identifier, passed, detail):
        checks.append({"id": identifier, "passed": bool(passed), "detail": detail})

    documents = {}
    for name in (*SOURCES, *VERSIONS):
        path = root / name
        try:
            documents[name] = path.read_text(encoding="utf-8")
            check("file:" + name, True, "readable UTF-8 file")
        except (OSError, UnicodeError) as error:
            documents[name] = ""
            check("file:" + name, False, str(error))

    for name, version in VERSIONS.items():
        text = documents[name]
        check("version:" + name, re.findall(r"^Version: (.+)$", text, re.M) == [version], version)
        check("design-only:" + name, re.findall(r"^Implementation: (.+)$", text, re.M) == ["DESIGN_ONLY"], "runtime remains unimplemented")

    architecture = documents["docs/architecture.md"]
    protocols = documents["docs/protocols.md"]
    adr = documents["docs/adr/ADR-0001.md"]
    charter = documents[SOURCES[0]]
    source = documents[SOURCES[1]]

    check("single-host", "Boundary: SINGLE_HOST_LINUX_WSL2" in architecture and "不承诺跨主机容错" in architecture, "single-host Linux/WSL2; no cross-host guarantee")
    glossary = table(section(architecture, "## 3. 术语"))
    check("glossary", {row[0] for row in glossary} == TERMS and all(len(row) == 2 and len(row[1]) >= 12 for row in glossary) and len(glossary) == len(TERMS), "17 distinct terms with definitions")

    expected = table(section(source, "## B. 最小持久化实体"))
    actual = table(section(architecture, "## 4. 持久化实体目录"))
    check("entities", len(expected) == 16 and actual == expected, "all 16 entities, fields/indexes and duties match 02.B")

    expected = [(key, value.replace("`", "")) for key, value in re.findall(r"^(I\d+)\. (.+)$", charter, re.M)]
    actual = table(section(architecture, "## 5. 冻结的十二项不变量"))
    check("invariants", len(expected) == 12 and [(row[0], row[1]) for row in actual if len(row) == 3] == expected and all("未实现" in row[-1] for row in actual), "I1-I12 match 01.6 without semantic rewriting")

    trust_rows = table(section(architecture, "## 1. 组件与信任边界"))
    trust = {row[0]: row[1] for row in trust_rows if len(row) == 2}
    check("trust-boundaries", set(trust) == set(TRUST) and all(all(token in trust.get(name, "") for token in tokens) for name, tokens in TRUST.items()), "authority, worker, model, reader, sandbox, verifier, aggregator and ledger boundaries")

    for heading, required, identifier in (
        ("## 1. Workflow 状态转移表", WORKFLOW, "workflow-transitions"),
        ("## 2. TaskInstance 状态转移表", TASK, "task-transitions"),
    ):
        edges = transitions(section(protocols, heading))
        check(identifier, set(edges) == required and len(edges) == len(required), "02.C main path, explicit cancellation and terminal closure; no added edges")
    exceptional = table(section(protocols, "## 3. 异常状态与恢复"))
    check("exceptional-states", len(exceptional) == 2 and {(row[0], row[1]) for row in exceptional if len(row) == 3} == {("blocked", "未指定"), ("interrupted", "未指定")} and "未规定路径默认拒绝" in protocols, "unspecified exception edges are not silently authorized")

    guards = section(protocols, "## 4. 受控写与事务责任")
    check("write-guards", all(token in guards for token in ("owner", "epoch", "lease_until > db_now", "expected_revision", "legal_transition", "current_plan_generation_if_relevant")), "all six controlled-write guards")
    limits = dict(re.findall(r"\b(MaxNodes|MaxDepth|MaxFanout)=(\d+)\b", protocols))
    check("plan-limits", limits == {"MaxNodes": "24", "MaxDepth": "6", "MaxFanout": "8"}, "02.D DAG bounds")
    recovery = section(protocols, "## 3. 异常状态与恢复")
    check("recovery", all(token in recovery for token in ("epoch+1", "model_calls", "tool_invocations", "unknown", "不默认重播", "保守上界")), "takeover fences prior owners and preserves unknown side effects")
    transactions = table(guards)
    check("transactions", {row[0] for row in transactions} == {"PlanCommit", "TaskResultCommit", "ClaimAndReserve", "HandoffConsume", "ModelCallReserve"} and all(len(row) == 3 and row[1] and row[2] for row in transactions), "07.A five transaction responsibilities, not implementations")

    cli_section = section(source, "## L. 对外接口（稳定协议，非具体实现）")
    expected_cli = [line.split("#", 1)[0].strip() for line in cli_section.splitlines() if line.startswith("mgarctl ")]
    actual_cli = [row[0] for row in table(section(protocols, "## 5. 对外接口索引"))]
    expected_internal = [
        "PlanStore.ProposeAndCommit(ctx, runID, baseVersion, proposal)",
        "Scheduler.GetReady(ctx, runID)", "Handoff.PublishTx(...)",
        "ContextBuilder.BuildSnapshot(...)", "Budget.ReserveModelCall(...)",
        "Verifier.Check(...)", "Agent.run(task, snapshot, tools, budget) -> AgentResult",
        "ExecutionAdapter",
    ]
    actual_internal = [row[0] for row in table(section(protocols, "## 6. 内部接口目录"))]
    check("interface-index", len(expected_cli) == 8 and actual_cli == expected_cli and actual_internal == expected_internal, "8 CLI entries and Go/Python/adapter design interfaces")
    check("adapter-decision", "DefaultAdapter: LocalSandboxAdapter" in adr and all(token in adr for token in ("原 Runtime 适配器", "可选集成", "进程/HTTP", "不复制原 Coding Runtime", "不修改 durable-coding-agent-runtime-source", "未实现", "代价 / 风险")), "standalone default, optional old-runtime integration, no copying")
    check("lock-order", LOCK_ORDER in adr, "Workflow -> lease -> budget -> task -> outbox, never inverse")

    index = table(section(documents["contracts/README.md"], "## 冻结索引（P0-01）"))
    names = ("plan", "artifact", "context_snapshot", "evidence", "verdict")
    check("schema-index", len(index) == len(names), "exactly five design schema entries")
    for name in names:
        relative = f"contracts/{name}.schema.json"
        try:
            schema = json.loads((root / relative).read_text(encoding="utf-8"))
            version = schema["properties"]["schema_version"]["const"]
            required = schema["required"]
            matches = [row for row in index if row[0] == f"[{name}.schema.json]({name}.schema.json)"]
            valid = (
                len(matches) == 1 and len(matches[0]) == 4
                and matches[0][1] == version
                and matches[0][2].split(", ") == required
                and re.fullmatch(r"P[0-8]-[0-9]{2}（(?:未实现|实际校验已实现)）", matches[0][3]) is not None
                and schema["$schema"] == "https://json-schema.org/draft/2020-12/schema"
                and schema["type"] == "object" and schema["additionalProperties"] is False
                and "schema_version" in required
                and set(required) <= set(schema["properties"])
            )
            check("schema:" + name, valid, relative + ": version, required list and object shape match index; not instance validation")
        except (OSError, ValueError, KeyError, TypeError) as error:
            check("schema:" + name, False, str(error))

    broken = []
    for name in VERSIONS:
        for destination in re.findall(r"(?<!!)\[[^\]]+\]\(([^)]+)\)", documents[name]):
            if re.match(r"https?://|mailto:|#", destination):
                continue
            destination = destination.split("#", 1)[0]
            target = ((root / name).parent / destination).resolve()
            if not target.is_relative_to(root.resolve()) or not target.is_file():
                broken.append(name + ": " + destination)
    check("local-links", not broken, broken or "all local document/schema/evidence links resolve inside checkout")
    violations = claim_violations({name: documents[name] for name in VERSIONS})
    check("claims", not violations, violations or "no detected mock-as-real, cross-host or external exactly-once assertions")
    failed = sum(not item["passed"] for item in checks)
    return {"scope": "P0-01 static design only", "checks": checks, "passed": len(checks) - failed, "failed": failed}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    report = validate(args.root.resolve())
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 1 if report["failed"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
