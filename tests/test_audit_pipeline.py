"""Acceptance must reject truncated real runner evidence, even after test PASS."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import xml.etree.ElementTree as ET

import pytest


def validator():
    path = Path(__file__).resolve().parents[1] / 'scripts/verify_results.py'
    spec = importlib.util.spec_from_file_location('audit_verify', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_audit_go_report_requires_package_completion(tmp_path):
    (tmp_path/'go.mod').write_text('module auditfixture\n\ngo 1.27.3\n')
    (tmp_path/'audit_test.go').write_text('package auditfixture\nimport "testing"\nfunc TestOK(t *testing.T) {}\n')
    run = subprocess.run(['go', 'test', '-count=1', '-json', '.'], cwd=tmp_path, capture_output=True, text=True)
    assert run.returncode == 0, run.stdout + run.stderr
    events = [json.loads(line) for line in run.stdout.splitlines()]
    assert validator().go_result(events)['status'] == 'PASS'
    final = next(i for i, e in enumerate(events) if e['Action'] == 'pass' and not e.get('Test'))
    assert validator().go_result(events[:final])['status'] == 'FAIL'


def test_audit_junit_report_rejects_missing_cases():
    xml = '<testsuites><testsuite tests="2" failures="0" errors="0" skipped="0"><testcase name="only_one" /></testsuite></testsuites>'
    assert validator().junit_result(ET.fromstring(xml))['status'] == 'FAIL'


def recorded_pytest(tmp_path, body):
    (tmp_path/'test_recorded.py').write_text(body)
    root = str(Path(__file__).resolve().parents[1])
    env = dict(os.environ, PYTHONPATH=root + os.pathsep + os.environ.get('PYTHONPATH', ''))
    run = subprocess.run([sys.executable, '-m', 'pytest', '-q', '-p', 'scripts.pytest_evidence',
        '--mgar-report=events.json', '--junitxml=result.xml'], cwd=tmp_path, env=env, capture_output=True, text=True)
    assert (tmp_path/'events.json').exists(), run.stdout + run.stderr
    return run, ET.parse(tmp_path/'result.xml').getroot(), json.loads((tmp_path/'events.json').read_text())


def test_audit_actual_pytest_subtests_and_missing_phase(tmp_path):
    run, xml, events = recorded_pytest(tmp_path, '''import unittest
class Recorded(unittest.TestCase):
    def test_subtests(self):
        for i in range(3):
            with self.subTest(i=i):
                self.assertGreater(i+1, 0)
''')
    assert run.returncode == 0, run.stdout + run.stderr
    assert validator().junit_result(xml, events)['status'] == 'PASS'
    assert sum(e['subtest'] for e in events['reports']) == 3
    missing_phase = json.loads(json.dumps(events))
    missing_phase['reports'] = [e for e in events['reports'] if e['when'] != 'teardown']
    assert validator().junit_result(xml, missing_phase)['status'] == 'FAIL'
    missing_subtest = json.loads(json.dumps(events))
    del missing_subtest['reports'][1]
    assert validator().junit_result(xml, missing_subtest)['status'] == 'FAIL'


@pytest.mark.parametrize('statement', ['pytest.skip("injected")', 'pytest.xfail("injected")', 'assert False'])
def test_audit_actual_subtest_failure_or_skip_is_rejected(tmp_path, statement):
    _, xml, events = recorded_pytest(tmp_path, 'import unittest,pytest\nclass Recorded(unittest.TestCase):\n'
        '    def test_subtests(self):\n        with self.subTest(i=1):\n            ' + statement + '\n')
    assert validator().junit_result(xml, events)['status'] == 'FAIL'
