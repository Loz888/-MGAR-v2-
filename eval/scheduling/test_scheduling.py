"""Execute the production Go ranking code with a fixed local timer workload."""
import json
import math
from pathlib import Path
import subprocess

import pytest

ROOT = Path(__file__).resolve().parents[2]


@pytest.fixture(scope="module")
def experiment(tmp_path_factory):
    binary = tmp_path_factory.mktemp("schedule") / "bench"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/schedulingbench"], cwd=ROOT, check=True)
    result = subprocess.run([str(binary), "--samples", "5"], capture_output=True, text=True, check=True, timeout=30)
    return json.loads(result.stdout)


def test_fixed_workload_observed_dependencies_and_fairness(experiment):
    assert experiment["concurrency"] == 2
    assert experiment["workload_kind"] == "actual_local_timers_not_LLM"
    assert len(experiment["trials"]) == 10
    for trial in experiment["trials"]:
        jobs = {j["key"]: j for j in trial["samples"]}
        assert len(jobs) == 13
        for job in jobs.values():
            assert 0 <= job["Ready"] <= job["Start"] < job["End"]
            for parent in job["parents"] or []:
                assert job["Start"] >= jobs[parent]["End"]
            assert job["End"] - job["Start"] >= job["Service"] * 0.95
        assert 0 < trial["Jain"] <= 1
        assert trial["MaxWait"] < trial["Makespan"]
        for moment in [j["Start"] + 0.000001 for j in jobs.values()]:
            assert sum(j["Start"] <= moment < j["End"] for j in jobs.values()) <= 2


def test_report_metrics_recompute_from_actual_samples(experiment):
    for trial in experiment["trials"]:
        ends = sorted(j["End"] for j in trial["samples"])
        assert trial["P50"] == ends[math.ceil(len(ends) * .5) - 1]
        assert trial["P95"] == ends[math.ceil(len(ends) * .95) - 1]
        assert trial["Throughput"] == pytest.approx(13_000 / max(ends))
        first = trial["samples"][0]["key"]
        assert first == ("A00" if trial["policy"] == "fifo" else "C00")
    # No unstable performance threshold: report regressions as observations.
    assert {t["policy"] for t in experiment["trials"]} == {"fifo", "critical_path"}
