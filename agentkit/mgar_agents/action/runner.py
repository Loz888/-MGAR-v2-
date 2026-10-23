"""Bounded owned-process execution, for trusted fixtures only; not a sandbox."""
from __future__ import annotations

import json
from collections.abc import Callable
import os
from pathlib import Path
import selectors
import signal
import subprocess
import time
from typing import Any

from mgar_agents.core.tools import PolicyDenied

EXPECTED_TEST = 'TestPaidDeduplicatedTotal'
PACKAGE = 'example.invalid/mgar/incident'


def capture(argv: list[str], cwd: Path, env: dict[str, str], timeout_s: float, output_limit: int,
            *, on_exit: Callable[[bytes, int, str | None], None] | None = None) -> tuple[bytes, int, str | None]:
    """Always kill/reap this process group, including on an SDK signal exception."""
    deadline = time.monotonic() + timeout_s
    chunks = bytearray()
    reason = None
    process = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True)
    assert process.stdout is not None
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    reason = 'timeout'
                    break
                if not selector.select(min(remaining, 0.1)):
                    continue
                chunk = os.read(process.stdout.fileno(), 4096)
                if not chunk:
                    selector.unregister(process.stdout)
                    break
                capacity = output_limit - len(chunks)
                chunks.extend(chunk[:capacity])
                if len(chunk) > capacity:
                    reason = 'output_limit'
                    break
            if reason is None:
                try:
                    process.wait(timeout=max(0.001, deadline - time.monotonic()))
                except subprocess.TimeoutExpired:
                    reason = 'timeout'
    except BaseException:
        reason = 'interrupted'
        raise
    finally:
        # A trusted child can have left descendants even after its own exit.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.stdout.close()
        process.wait()
        if on_exit is not None:
            on_exit(bytes(chunks), process.returncode, reason)
    return bytes(chunks), process.returncode, reason


def parse_go_events(raw: str, exit_code: int) -> dict[str, Any]:
    """Require the fixed test's real run/terminal and package completion events."""
    started = False
    package_done = False
    active = False
    terminal = None
    skipped = 0
    invalid = False
    ground_truth = ''
    try:
        for line in raw.splitlines():
            event = json.loads(line)
            if not isinstance(event, dict) or event.get('Package') != PACKAGE or package_done:
                raise ValueError('unexpected event')
            action, test = event.get('Action'), event.get('Test')
            if action == 'start' and test is None:
                if started:
                    raise ValueError('duplicate package start')
                started = True
            elif action == 'run' and test == EXPECTED_TEST:
                if not started or active or terminal:
                    raise ValueError('duplicate or unordered test')
                active = True
            elif action == 'output':
                output = event.get('Output', '')
                if not isinstance(output, str):
                    raise ValueError('invalid output')
                for value in (320, 220, 150):
                    if f'got={value}' in output:
                        ground_truth = f'got={value}'
            elif action in {'pass', 'fail', 'skip'}:
                skipped += action == 'skip'
                if test == EXPECTED_TEST:
                    if not active or terminal:
                        raise ValueError('test terminal without run')
                    terminal = action
                    active = False
                elif test is None:
                    if not started or not terminal or active:
                        raise ValueError('incomplete package')
                    package_done = True
                    invalid |= action != 'pass'
                else:
                    raise ValueError('unexpected test')
            else:
                raise ValueError('unsupported event')
    except (ValueError, TypeError):
        invalid = True
    return {'passed': exit_code == 0 and started and package_done and terminal == 'pass' and not invalid and skipped == 0,
        'tests_run': [EXPECTED_TEST] if terminal else [], 'skipped': skipped,
        'complete': package_done and not active, 'ground_truth_line': ground_truth}


def validate_duration(value: float) -> None:
    import math
    if type(value) not in {int, float} or not math.isfinite(value) or not 0 < value <= 300:
        raise PolicyDenied('invalid action deadline')
