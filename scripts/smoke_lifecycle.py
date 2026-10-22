"""Exercise the actual CLI/daemon binaries against the selected synthetic DB."""
import json
import subprocess


def call(*args, expected=0):
    result = subprocess.run(args, text=True, capture_output=True)
    if result.returncode != expected:
        raise RuntimeError(f"{args[0]} exit {result.returncode}: {result.stderr}")
    return result.stdout


subprocess.run(["go", "build", "-o", "bin/mgarctl", "./cmd/mgarctl"], check=True)
subprocess.run(["go", "build", "-o", "bin/orchestratord", "./cmd/orchestratord"], check=True)
created = json.loads(call("bin/mgarctl", "submit", "--fixture", "incident", "--provider", "mock"))
run_id = created["run_id"]
assert created["state"] == "running" and created["active_plan_version"] == 1
try:
    call("bin/orchestratord", "--once")
    queried = json.loads(call("bin/mgarctl", "status", run_id))
    assert queried["run_id"] == run_id and queried["finished_at"] is None
finally:
    # Only this smoke's own synthetic run is cancelled; history is preserved.
    cancelled = json.loads(call("bin/mgarctl", "cancel", run_id))
assert cancelled["state"] == "cancelled" and cancelled["finished_at"] is not None
call("bin/mgarctl", "resume", run_id, expected=1)
call("bin/mgarctl", "submit", "--run-id", run_id, expected=1)
frozen = json.loads(call("bin/mgarctl", "status", run_id))
assert frozen == cancelled
call("bin/mgarctl", "submit", "--provider", "real", expected=2)
print(json.dumps({"run_id": run_id, "submit": "running", "restart_scan": "completed", "cancel": "cancelled", "terminal_immutable": True, "paid_provider": "rejected"}))
