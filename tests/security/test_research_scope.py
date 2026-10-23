"""Independent Research source-scope and observed-citation regressions."""
import hashlib
import json
import os
from pathlib import Path
import stat
import time
from uuid import uuid4

import pytest

from mgar_agents.core.claim import Claim
from mgar_agents.core.provider import MockProvider
from mgar_agents.core.schemas import Budget, ContextSnapshot, Task


TEXT = "Change memo: aggregation retries introduced duplicate order rows.\nUse only observed source text as evidence.\n"
SOURCE = "change_note"


def task_inputs():
    claim = Claim(str(uuid4()), str(uuid4()), "research-scope-worker", "research", 1, 0, 1)
    task = Task(claim, "Investigate the synthetic change memo", "research-scope")
    snapshot = ContextSnapshot.from_dict({"schema_version": "context.v1", "task_id": claim.task_id,
                                         "plan_version": 1, "model_config_version": "mock.v1",
                                         "tool_policy_version": "research-scope.v1", "artifacts": []})
    return task, snapshot


def decision(kind, **fields):
    return json.dumps({"type": kind, **fields})


def citation_output(*, verified=False):
    output = {"statement": "The change memo describes duplicate rows.",
              "citations": [{"source_id": SOURCE, "content_sha256": hashlib.sha256(TEXT.encode()).hexdigest(),
                             "quote": TEXT.splitlines()[0]}]}
    if verified:
        output["verified"] = True
    return output


def read_then_cite(request):
    if request.turn == 1:
        return decision("tool_call", call_id="read_change", name="research.read", arguments={"source_id": SOURCE})
    observed = request.observations[-1].output
    assert observed["text"] == TEXT
    return decision("final", output={"statement": "The observed change memo describes duplicate rows.",
                                     "citations": [{"source_id": observed["source_id"],
                                                    "content_sha256": observed["content_sha256"],
                                                    "quote": observed["text"].splitlines()[0]}]})


def make_index(tmp_path, relative="change.md"):
    from mgar_agents.research import SourceIndex, SourceSpec
    root = tmp_path / "sources"
    root.mkdir()
    path = root / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(TEXT)
    spec = SourceSpec(SOURCE, relative, "doc", "reference", hashlib.sha256(TEXT.encode()).hexdigest(), "Synthetic change")
    return SourceIndex(root, [spec], version="scope.v1"), root, path


def run_research(index, responses, *, budget=None):
    from mgar_agents.research import ResearchAgent
    task, snapshot = task_inputs()
    return ResearchAgent(MockProvider(responses), index).run(task, snapshot, budget=budget or Budget())


def test_authorized_observed_source_has_a_real_positive_control(tmp_path):
    index, _, _ = make_index(tmp_path)
    result = run_research(index, read_then_cite)
    assert result.status == "completed" and result.turns == 2
    assert result.verified is False and result.to_dict()["verified"] is False
    assert result.observations[0].output["content_sha256"] == hashlib.sha256(TEXT.encode()).hexdigest()
    assert result.observations[0].output["text"] == TEXT


def test_a_later_task_cannot_borrow_prior_task_read_history(tmp_path):
    index, _, _ = make_index(tmp_path)
    first = run_research(index, read_then_cite)
    assert first.status == "completed"
    serialized = first.to_dict()
    second = run_research(index, [decision("final", output=citation_output())])
    assert second.status == "failed", "fresh task used a prior task's source-read authorization"
    assert second.observations == () and second.verified is False
    assert first.to_dict() == serialized


def test_forged_verified_property_does_not_override_research_output(tmp_path):
    index, _, _ = make_index(tmp_path)

    def choose(request):
        if request.turn == 1:
            return decision("tool_call", call_id="read_change", name="research.read", arguments={"source_id": SOURCE})
        return decision("final", output=citation_output(verified=True))

    result = run_research(index, choose)
    assert result.status == "failed" and result.verified is False


@pytest.mark.parametrize("replacement", ["changed", "final_symlink", "directory_symlink", "root_symlink", "fifo"])
def test_file_replacement_cannot_escape_or_skip_hash_validation(tmp_path, replacement, monkeypatch):
    index, root, path = make_index(tmp_path, "docs/change.md")
    marker = "UNAUTHORIZED-OUTSIDE-CONTENT"
    outside = tmp_path / "outside"
    outside.mkdir()
    (outside / "change.md").write_text(marker)
    (outside / "docs").mkdir()
    (outside / "docs/change.md").write_text(marker)
    if replacement == "changed":
        path.write_text(marker)
    elif replacement == "final_symlink":
        path.unlink()
        path.symlink_to(outside / "change.md")
    elif replacement == "directory_symlink":
        original = root / "docs"
        original.rename(root / "original_docs")
        original.symlink_to(outside, target_is_directory=True)
    elif replacement == "root_symlink":
        root.rename(tmp_path / "original_root")
        root.symlink_to(outside, target_is_directory=True)
    else:
        path.unlink()
        os.mkfifo(path)
    outside_opens = []
    original_open = os.open

    def checked_open(name, flags, mode=0o777, *, dir_fd=None):
        fd = original_open(name, flags, mode, dir_fd=dir_fd)
        resolved = Path(os.readlink(f"/proc/self/fd/{fd}"))
        if stat.S_ISREG(os.fstat(fd).st_mode) and resolved.is_relative_to(outside):
            outside_opens.append(str(resolved))
        return fd

    monkeypatch.setattr(os, "open", checked_open)
    before = time.monotonic()
    result = run_research(index, read_then_cite, budget=Budget(timeout_s=.2))
    assert time.monotonic() - before < .5, "special file blocked a supposedly bounded source read"
    assert result.status == "failed" and result.error_kind == "policy_denied"
    assert marker not in json.dumps(result.to_dict())
    assert result.observations == ()
    assert outside_opens == [], "out-of-scope regular file was opened before its content hash was checked"


def test_citation_quote_must_come_from_the_observed_bytes(tmp_path):
    index, _, _ = make_index(tmp_path)

    def choose(request):
        if request.turn == 1:
            return decision("tool_call", call_id="read_change", name="research.read", arguments={"source_id": SOURCE})
        output = citation_output()
        output["citations"][0]["quote"] = "This never appeared in the source."
        return decision("final", output=output)

    result = run_research(index, choose)
    assert result.status == "failed" and result.verified is False


def test_model_path_and_execution_requests_cannot_become_capabilities(tmp_path):
    index, _, _ = make_index(tmp_path)
    for name, arguments in [("research.read", {"source_id": "../outside"}),
                            ("research.read", {"source_id": SOURCE, "path": "../outside"}),
                            ("exec", {"command": "printf SHOULD_NEVER_RUN"})]:
        result = run_research(index, [decision("tool_call", call_id="bad_scope", name=name, arguments=arguments)])
        assert result.status == "failed" and result.error_kind == "policy_denied"
        assert result.observations == ()
