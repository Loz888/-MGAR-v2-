"""Reject failures, skips, xfails, incomplete and zero-test enabled suites."""
import argparse
import json
import xml.etree.ElementTree as ET
from pathlib import Path


def go_result(events):
    running = set()
    finished = set()
    passed = failed = skipped = 0
    for e in events:
        key = (e.get("Package"), e.get("Test"))
        action = e.get("Action")
        if action == "fail":
            failed += 1
        if not e.get("Test"):
            continue
        if action == "run":
            running.add(key)
        if action in {"pass", "fail", "skip"}:
            finished.add(key)
            passed += action == "pass"
            skipped += action == "skip"
    status = "FAIL" if failed or skipped or running != finished else "PASS"
    if not running and not failed:
        status = "NOT_RUN"
    return {"status": status, "tests": len(running), "passed": passed, "failed_events": failed, "skipped": skipped}


def junit_result(root):
    cases = list(root.iter("testcase"))
    failed = sum(c.find("failure") is not None or c.find("error") is not None for c in cases)
    skipped = sum(c.find("skipped") is not None for c in cases)
    status = "FAIL" if failed or skipped else "PASS"
    if not cases:
        status = "NOT_RUN"
    return {"status": status, "tests": len(cases), "passed": len(cases) - failed - skipped, "failed": failed, "skipped_or_xfailed": skipped}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--go", type=Path, required=True)
    parser.add_argument("--junit", type=Path, required=True)
    args = parser.parse_args()
    try:
        report = {"go": go_result([json.loads(s) for s in args.go.read_text().splitlines()]), "python": junit_result(ET.parse(args.junit).getroot())}
        report["status"] = "PASS" if all(v["status"] == "PASS" for v in report.values()) else "FAIL"
    except (OSError, ValueError, ET.ParseError) as error:
        report = {"status": "FAIL", "error": str(error)}
    print(json.dumps(report, indent=2))
    return 0 if report["status"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
