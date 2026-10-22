"""P0-01: exercise the real validator against valid and damaged document trees."""

import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
VALIDATOR = ROOT / "scripts/validate_plan_docs.py"
DOCUMENTS = (
    "01_PROJECT_CHARTER.md",
    "02_ARCHITECTURE_CONTRACTS.md",
    "03_CODEX_WORK_PROTOCOL.md",
    "07_IMPLEMENTATION_PLAYBOOK.md",
    "docs/architecture.md",
    "docs/protocols.md",
    "docs/development.md",
    "docs/limitations.md",
    "docs/adr/ADR-0001.md",
    "docs/adr/ADR-0002.md",
    "contracts/README.md",
)


class PlanDocsTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="mgar-p0-01-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        for name in DOCUMENTS:
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            if (ROOT / name).exists():
                shutil.copyfile(ROOT / name, target)
        for source in (ROOT / "contracts").glob("*.schema.json"):
            shutil.copyfile(source, self.root / "contracts" / source.name)

    def replace(self, name, old, new):
        path = self.root / name
        text = path.read_text(encoding="utf-8")
        self.assertIn(old, text, "mutation must actually change the fixture")
        path.write_text(text.replace(old, new, 1), encoding="utf-8")

    def run_validator(self, failure=None):
        result = subprocess.run(
            [sys.executable, str(VALIDATOR), "--root", str(self.root)],
            cwd=self.root,
            text=True,
            capture_output=True,
            check=False,
        )
        if failure is None:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        report = json.loads(result.stdout)
        failed = {item["id"] for item in report["checks"] if not item["passed"]}
        if failure is not None:
            self.assertIn(failure, failed, result.stdout)
        self.assertGreater(len(report["checks"]), 0)
        return report

    def test_valid_documents(self):
        report = self.run_validator()
        self.assertEqual(report["failed"], 0)

    def test_default_root_does_not_depend_on_cwd(self):
        result = subprocess.run(
            [sys.executable, str(VALIDATOR)], cwd=self.root,
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_missing_document_is_rejected(self):
        (self.root / "docs/architecture.md").unlink()
        self.run_validator("file:docs/architecture.md")

    def test_missing_version_is_rejected(self):
        self.replace("docs/protocols.md", "Version: protocols.v1", "Version: unknown")
        self.run_validator("version:docs/protocols.md")

    def test_implemented_claim_is_rejected(self):
        self.replace("docs/architecture.md", "Implementation: DESIGN_ONLY", "Implementation: IMPLEMENTED")
        self.run_validator("design-only:docs/architecture.md")

    def test_missing_term_is_rejected(self):
        self.replace("docs/architecture.md", "| Worker |", "| RenamedWorker |")
        self.run_validator("glossary")

    def test_missing_entity_field_is_rejected(self):
        self.replace("docs/architecture.md", "active_plan_version, revision", "active_plan_version")
        self.run_validator("entities")

    def test_changed_invariant_is_rejected(self):
        self.replace("docs/architecture.md", "过期 Owner 永远不能提交结果", "过期 Owner 可以提交结果")
        self.run_validator("invariants")

    def test_missing_trust_boundary_is_rejected(self):
        self.replace("docs/architecture.md", "| Sandbox |", "| UnrestrictedProcess |")
        self.run_validator("trust-boundaries")

    def test_dag_limit_drift_is_rejected(self):
        self.replace("docs/protocols.md", "MaxNodes=24", "MaxNodes=240")
        self.run_validator("plan-limits")

    def test_missing_fencing_guard_is_rejected(self):
        self.replace("docs/protocols.md", "expected_revision", "unused_revision")
        self.run_validator("write-guards")

    def test_missing_state_edge_is_rejected(self):
        self.replace("docs/protocols.md", "| pending | ready |", "| pending | executing |")
        self.run_validator("task-transitions")

    def test_terminal_reopening_is_rejected(self):
        self.replace("docs/protocols.md", "| succeeded | ∅ |", "| succeeded | running |")
        self.run_validator("workflow-transitions")

    def test_exceptional_states_cannot_be_silently_enabled(self):
        self.replace("docs/protocols.md", "| interrupted | 未指定 |", "| interrupted | running |")
        self.run_validator("exceptional-states")

    def test_recovery_must_advance_epoch(self):
        self.replace("docs/protocols.md", "epoch+1", "epoch不变")
        self.run_validator("recovery")

    def test_local_adapter_decision_is_required(self):
        self.replace("docs/adr/ADR-0001.md", "DefaultAdapter: LocalSandboxAdapter", "DefaultAdapter: RuntimeAdapter")
        self.run_validator("adapter-decision")

    def test_lock_order_drift_is_rejected(self):
        self.replace("docs/adr/ADR-0001.md", "workflow_runs → orchestrator_leases → run_budgets → task_instances → outbox_events", "task_instances → run_budgets → workflow_runs → outbox_events")
        self.run_validator("lock-order")

    def test_missing_interface_is_rejected(self):
        self.replace("docs/protocols.md", "mgarctl resume <run-id>", "mgarctl unknown <run-id>")
        self.run_validator("interface-index")

    def test_missing_schema_file_is_rejected(self):
        (self.root / "contracts/evidence.schema.json").unlink()
        self.run_validator("schema:evidence")

    def test_schema_index_version_must_match(self):
        self.replace("contracts/README.md", "evidence.v1", "evidence.v2")
        self.run_validator("schema:evidence")

    def test_schema_index_required_fields_must_match(self):
        self.replace("contracts/README.md", "input_snapshot_id, tool_version", "tool_version")
        self.run_validator("schema:evidence")

    def test_broken_local_link_is_rejected(self):
        self.replace("docs/architecture.md", "../01_PROJECT_CHARTER.md", "../missing-charter.md")
        self.run_validator("local-links")

    def test_prohibited_claims_are_rejected(self):
        claims = (
            "Mock 就是真实 Agent。",
            "MockProvider proves real agent capabilities.",
            "系统保证跨主机容错。",
            "We guarantee cross-host fault tolerance.",
            "保证外部 Exactly-once。",
            "External exactly-once is guaranteed.",
            "不承诺跨主机容错；但本系统保证跨主机容错。",
        )
        path = self.root / "docs/architecture.md"
        original = path.read_text(encoding="utf-8")
        for claim in claims:
            with self.subTest(claim=claim):
                path.write_text(original + "\n" + claim + "\n", encoding="utf-8")
                self.run_validator("claims")

    def test_disclaimers_are_not_prohibited_claims(self):
        path = self.root / "docs/architecture.md"
        with path.open("a", encoding="utf-8") as stream:
            stream.write("\n不承诺跨主机容错。\n不保证外部 exactly-once。\nMock 不等于真实 Agent。\n")
        self.run_validator()


if __name__ == "__main__":
    unittest.main(verbosity=2)
