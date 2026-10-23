"""Record actual pytest 9 phases/subtests without assertion text or parameters.

The locked pytest JUnit writer counts passed subtests without emitting separate
testcase elements. This independent event record makes that difference auditable.
"""
import json
import hashlib
from pathlib import Path

from _pytest.subtests import SubtestReport


def pytest_addoption(parser):
    parser.addoption('--mgar-report', help='write actual test phase evidence')


def pytest_configure(config):
    destination = config.getoption('--mgar-report')
    if destination:
        config.pluginmanager.register(EvidenceRecorder(Path(destination)), 'mgar-test-evidence')


class EvidenceRecorder:
    def __init__(self, destination):
        self.destination = destination
        self.collected = []
        self.reports = []

    def pytest_collection_finish(self, session):
        self.collected = [hashlib.sha256(item.nodeid.encode()).hexdigest() for item in session.items]

    def pytest_runtest_logreport(self, report):
        self.reports.append({'test_id': hashlib.sha256(report.nodeid.encode()).hexdigest(), 'when': report.when, 'outcome': report.outcome,
            'subtest': isinstance(report, SubtestReport), 'xfail': hasattr(report, 'wasxfail')})

    def pytest_sessionfinish(self, session, exitstatus):
        self.destination.write_text(json.dumps({'schema_version': 'pytest_evidence.v1', 'exit_code': int(exitstatus),
            'collected': self.collected, 'reports': self.reports}, ensure_ascii=False, indent=2) + '\n')
