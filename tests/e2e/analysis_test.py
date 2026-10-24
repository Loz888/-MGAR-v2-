from tests.e2e.incident_test import assert_success, run_demo


def test_analysis_readonly_full_control_plane():
    report = run_demo('offline-002')
    assert_success(report, 'offline-002', False)
    assert report['sandbox_executions'] == 0
    assert all(t['role'] != 'action' for t in report['audit']['tasks'])


def test_policy_denied_no_action():
    report = run_demo('offline-055')
    assert report['state'] == 'blocked'
    assert report['answer']['reason'] == 'policy_denied'
    assert report['sandbox_executions'] == 0
