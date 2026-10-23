"""Execution boundary probes for the trusted local Action adapter."""
import json
import os
from pathlib import Path
import signal
import sys

import pytest

from mgar_agents.action.runner import PACKAGE, EXPECTED_TEST, capture, parse_go_events
from mgar_agents.core.tools import run_bounded


def events(actions):
    return '\n'.join(json.dumps({'Package': PACKAGE, 'Action': action, **({'Test': EXPECTED_TEST} if test else {})})
        for action, test in actions)


def test_go_parser_has_complete_positive_control():
    raw = events([('start', False), ('run', True), ('pass', True), ('pass', False)])
    assert parse_go_events(raw, 0)['passed'] is True
    assert parse_go_events(raw, 1)['passed'] is False


@pytest.mark.parametrize('actions', [[], [('start', False), ('pass', False)],
    [('start', False), ('run', True), ('skip', True), ('pass', False)],
    [('start', False), ('run', True), ('pass', True)],
    [('pass', True), ('pass', False)],
    [('start', False), ('run', True), ('fail', True), ('pass', False)],
    [('start', False), ('run', True), ('run', True), ('pass', True), ('pass', False)],
    [('start', False), ('run', True), ('pass', True), ('pass', False), ('fail', False)]])
def test_go_parser_rejects_zero_skipped_incomplete_or_forged_sequences(actions):
    assert parse_go_events(events(actions), 0)['passed'] is False


@pytest.mark.parametrize('raw', ['PASS', '{"Action":"pass"}', '[]', '{invalid',
    events([('start', False), ('run', True), ('pass', True), ('pass', False)]).replace(PACKAGE, 'foreign/package')])
def test_go_parser_rejects_plain_claims_and_foreign_events(raw):
    assert parse_go_events(raw, 0)['passed'] is False


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    status = Path(f'/proc/{pid}/stat')
    return status.exists() and status.read_text().split()[2] != 'Z'


def test_capture_owns_and_reaps_timeout_process(tmp_path):
    raw, code, reason = capture([sys.executable, '-c', 'import os,time;print(os.getpid(),flush=True);time.sleep(30)'],
        tmp_path, {'PATH': '/usr/bin:/bin'}, 0.15, 1024)
    assert reason == 'timeout' and code == -signal.SIGKILL
    assert not alive(int(raw.decode().strip()))


def test_capture_enforces_streaming_output_cap(tmp_path):
    raw, code, reason = capture([sys.executable, '-c', 'import os;\nwhile True: os.write(1,b"x"*4096)'],
        tmp_path, {'PATH': '/usr/bin:/bin'}, 2, 1024)
    assert reason == 'output_limit' and code == -signal.SIGKILL and len(raw) == 1024


def test_capture_timeout_stops_owned_descendant_group(tmp_path):
    script = ('import os,subprocess,sys; '
        'child=subprocess.Popen([sys.executable,"-c","import time;time.sleep(30)"]); '
        'print(os.getpid(),child.pid,flush=True);child.wait()')
    raw, code, reason = capture([sys.executable, '-c', script], tmp_path, {'PATH': '/usr/bin:/bin'}, 0.2, 1024)
    assert reason == 'timeout' and code == -signal.SIGKILL
    parent, child = map(int, raw.decode().split())
    assert not alive(parent) and not alive(child)


def test_outer_sdk_timeout_still_records_actual_exit_and_reaps_process(tmp_path):
    recorded = []
    def record(raw, code, reason):
        recorded.append((raw, code, reason))
    with pytest.raises(TimeoutError):
        run_bounded(lambda: capture([sys.executable, '-c', 'import os,time;print(os.getpid(),flush=True);time.sleep(30)'],
            tmp_path, {'PATH': '/usr/bin:/bin'}, 30, 1024, on_exit=record), 0.15)
    assert len(recorded) == 1
    raw, code, reason = recorded[0]
    assert reason == 'interrupted' and code == -signal.SIGKILL
    assert not alive(int(raw.decode().strip()))
