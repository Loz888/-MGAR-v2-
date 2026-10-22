"""Acceptance fingerprints cover actual Git source inputs, not generated evidence."""

from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "scripts/source_fingerprint.py"


class FingerprintTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="mgar-fingerprint-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.git("init", "--quiet")
        self.write(".gitignore", ".local/\n__pycache__/\n.venv/\n.env\n")
        self.write("internal/example.go", "package example\n")
        self.write("contracts/plan.schema.json", '{"version": 1}\n')
        self.write("docs/architecture.md", "# Frozen architecture\n")
        self.write("compose.yaml", "services:\n  db:\n    image: postgres:16\n")
        self.write("TASK_QUEUE.md", "P1-05: IN_PROGRESS\n")
        self.git("add", ".")

    def git(self, *arguments):
        result = subprocess.run(
            ["git", *arguments], cwd=self.root,
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout

    def write(self, name, content):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")

    def fingerprint(self):
        result = subprocess.run(
            [sys.executable, str(SCRIPT)], cwd=self.root,
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        value = result.stdout.strip()
        self.assertRegex(value, re.compile(r"\A[0-9a-f]{64}\Z"))
        return value

    def test_contract_document_and_compose_changes_invalidate_evidence(self):
        for name, content in (
            ("contracts/plan.schema.json", '{"version": 2}\n'),
            ("docs/architecture.md", "# Revised trust boundary\n"),
            ("compose.yaml", "services:\n  db:\n    image: postgres:17\n"),
        ):
            with self.subTest(path=name):
                before = self.fingerprint()
                self.write(name, content)
                self.assertNotEqual(before, self.fingerprint(), name)

    def test_untracked_candidate_inputs_are_included(self):
        before = self.fingerprint()
        self.write("contracts/new schema.json", '{"required": ["epoch"]}\n')
        candidate = self.fingerprint()
        self.assertNotEqual(before, candidate)
        self.git("add", "contracts/new schema.json")
        self.assertEqual(candidate, self.fingerprint(), "staging is not an input change")

    def test_evidence_and_queue_status_do_not_change_the_fingerprint(self):
        before = self.fingerprint()
        self.write("evidence/batches/P1-05/accept.json", '{"status": "PASS"}\n')
        self.write("evidence/runs/first/fingerprint", before + "\n")
        self.write("TASK_QUEUE.md", "P1-05: COMPLETE\n")
        self.assertEqual(before, self.fingerprint())
        self.git("add", "evidence", "TASK_QUEUE.md")
        self.assertEqual(before, self.fingerprint(), "tracked evidence also stays excluded")

    def test_ignored_local_outputs_do_not_change_the_fingerprint(self):
        before = self.fingerprint()
        for name in (".local/database.env", "__pycache__/fingerprint.pyc", ".venv/bin/python", ".env"):
            self.write(name, "synthetic local output\n")
        self.assertEqual(before, self.fingerprint())

    def test_repeated_evaluation_and_git_metadata_are_stable(self):
        before = self.fingerprint()
        self.assertEqual(before, self.fingerprint())
        self.git("-c", "user.name=Fingerprint Fixture", "-c", "user.email=fixture@example.invalid",
                 "commit", "--quiet", "-m", "synthetic fixture")
        self.assertEqual(before, self.fingerprint())


if __name__ == "__main__":
    unittest.main(verbosity=2)
