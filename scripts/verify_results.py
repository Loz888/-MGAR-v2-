"""Reject failures, skips, xfails, incomplete and zero-test enabled suites."""
import argparse
import json
import xml.etree.ElementTree as ET
from pathlib import Path


def go_result(events):
    running = set()
    finished = set()
    packages = set()
    completed = set()
    invalid = False
    passed = failed = skipped = 0
    for e in events:
        if not isinstance(e, dict) or not isinstance(e.get("Package"), str) or not e["Package"]:
            invalid = True
            continue
        package = e["Package"]
        key = (e.get("Package"), e.get("Test"))
        action = e.get("Action")
        if action in {"fail", "build-fail"}:
            failed += 1
        if action in {"build-output", "build-fail"}:
            continue
        if package in completed:
            invalid = True
        if not e.get("Test"):
            if action == "start":
                invalid |= package in packages
                packages.add(package)
            elif action in {"pass", "fail", "skip"}:
                invalid |= package not in packages or any(p == package and (p, name) not in finished for p, name in running)
                invalid |= action == "skip" and any(p == package for p, _ in running)
                completed.add(package)
            continue
        invalid |= package not in packages
        if action == "run":
            invalid |= key in running
            running.add(key)
        if action in {"pass", "fail", "skip"}:
            invalid |= key not in running or key in finished
            finished.add(key)
            passed += action == "pass"
            skipped += action == "skip"
    complete = not invalid and running == finished and packages == completed
    status = "FAIL" if failed or skipped or not complete else "PASS"
    if not running and not failed:
        status = "NOT_RUN"
    return {"status": status, "tests": len(running), "passed": passed, "failed_events": failed, "skipped": skipped, "complete": complete}


def pytest_result(evidence):
    """Require every collected case's successful setup/call/teardown and subtests."""
    result = {"status": "FAIL", "tests": 0, "passed_subtests": 0}
    try:
        names = evidence["collected"]
        if (evidence["schema_version"] != "pytest_evidence.v1" or type(evidence["exit_code"]) is not int
                or evidence["exit_code"] != 0 or not isinstance(names, list)
                or not all(isinstance(n, str) and n for n in names) or len(set(names)) != len(names)
                or not isinstance(evidence["reports"], list)):
            return result
        phases = {name: [] for name in names}
        result["tests"] = len(names)
        for report in evidence["reports"]:
            node, when = report["test_id"], report["when"]
            if (node not in phases or report["outcome"] != "passed" or report["xfail"] is not False
                    or type(report["subtest"]) is not bool):
                return result
            if report["subtest"]:
                if when != "call" or phases[node] != ["setup"]:
                    return result
                result["passed_subtests"] += 1
            else:
                phases[node].append(when)
        if all(value == ["setup", "call", "teardown"] for value in phases.values()):
            result["status"] = "PASS" if names else "NOT_RUN"
    except (KeyError, TypeError, AttributeError):
        pass
    return result


def junit_result(root, evidence=None):
    cases = list(root.iter("testcase"))
    failed = sum(c.find("failure") is not None or c.find("error") is not None for c in cases)
    skipped = sum(c.find("skipped") is not None for c in cases)
    suites = list(root.iter("testsuite"))
    complete = root.tag in {"testsuites", "testsuite"} and bool(suites)
    subtests = 0
    if evidence is not None:
        runner = pytest_result(evidence)
        complete &= runner["status"] == "PASS" and runner["tests"] == len(cases) and len(suites) == 1
        subtests = runner["passed_subtests"]
    for suite in suites:
        actual = list(suite.iter("testcase"))
        counts = {"tests": len(actual) + subtests, "failures": sum(c.find("failure") is not None for c in actual),
                  "errors": sum(c.find("error") is not None for c in actual), "skipped": sum(c.find("skipped") is not None for c in actual)}
        for name, count in counts.items():
            try:
                complete &= int(suite.get(name, "-1" if name == "tests" else "0")) == count
            except ValueError:
                complete = False
    status = "FAIL" if failed or skipped or not complete else "PASS"
    if not cases:
        status = "NOT_RUN"
    return {"status": status, "tests": len(cases), "passed": len(cases) - failed - skipped, "failed": failed, "skipped_or_xfailed": skipped, "complete": complete, "passed_subtests": subtests}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--go", type=Path, required=True)
    parser.add_argument("--junit", type=Path, required=True)
    parser.add_argument("--pytest-events", type=Path)
    args = parser.parse_args()
    try:
        evidence = None if args.pytest_events is None else json.loads(args.pytest_events.read_text())
        report = {"go": go_result([json.loads(s) for s in args.go.read_text().splitlines()]), "python": junit_result(ET.parse(args.junit).getroot(), evidence)}
        report["status"] = "PASS" if all(v["status"] == "PASS" for v in report.values()) else "FAIL"
    except (OSError, ValueError, ET.ParseError) as error:
        report = {"status": "FAIL", "error": str(error)}
    print(json.dumps(report, indent=2))
    return 0 if report["status"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
