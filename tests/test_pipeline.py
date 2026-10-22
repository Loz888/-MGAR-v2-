import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]


class PipelineTests(unittest.TestCase):
    def validator(self):
        spec = importlib.util.spec_from_file_location("verify", ROOT / "scripts/verify_results.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_empty_go_is_not_pass(self):
        report = self.validator().go_result([{"Action": "pass", "Package": "empty"}])
        self.assertEqual(report["status"], "NOT_RUN")

    def test_go_fail_and_skip_are_rejected(self):
        for statement, outcome in [('t.Fatal("injected")', "fail"), ('t.Skip("injected")', "skip")]:
            with self.subTest(outcome=outcome), tempfile.TemporaryDirectory() as d:
                p = Path(d)
                (p / "go.mod").write_text("module fixture\n\ngo 1.27.3\n")
                (p / "fixture_test.go").write_text('package fixture\nimport "testing"\nfunc TestInjected(t *testing.T){' + statement + '}\n')
                result = subprocess.run(["go", "test", "-json", "."], cwd=p, text=True, capture_output=True)
                events = [json.loads(line) for line in result.stdout.splitlines()]
                self.assertTrue(any(e.get("Action") == outcome and e.get("Test") == "TestInjected" for e in events), result.stdout + result.stderr)
                self.assertEqual(self.validator().go_result(events)["status"], "FAIL")

    def test_pytest_failure_skip_xfail_are_rejected(self):
        for statement in ['assert False', 'pytest.skip("injected")', 'pytest.xfail("injected")']:
            with self.subTest(statement=statement), tempfile.TemporaryDirectory() as d:
                p = Path(d)
                (p / "test_injected.py").write_text("import pytest\ndef test_injected():\n    " + statement + "\n")
                r = subprocess.run([sys.executable, "-m", "pytest", "-q", "--junitxml=result.xml"], cwd=p, text=True, capture_output=True)
                self.assertTrue((p / "result.xml").exists(), r.stdout + r.stderr)
                self.assertEqual(self.validator().junit_result(ET.parse(p / "result.xml").getroot())["status"], "FAIL")

    def test_zero_pytest_is_not_pass(self):
        result = self.validator().junit_result(ET.fromstring('<testsuites><testsuite tests="0" /></testsuites>'))
        self.assertEqual(result["status"], "NOT_RUN")

    def test_incomplete_go_is_rejected(self):
        self.assertEqual(self.validator().go_result([{"Action": "run", "Test": "TestIncomplete", "Package": "x"}])["status"], "FAIL")
