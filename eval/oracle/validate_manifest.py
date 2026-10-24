"""Validate immutable dataset inputs and independent truth; never repairs files."""
from __future__ import annotations

from collections import Counter
import hashlib
import json
from pathlib import Path
import re

from reference import CATEGORIES, VERSION, canonical, expected, sha

ROOT = Path(__file__).resolve().parents[2]


def read(root, relative):
    if not isinstance(relative, str) or relative.startswith("/") or ".." in Path(relative).parts:
        raise ValueError("INVALID_MANIFEST_PATH")
    path = root / relative
    if path.resolve() != path.absolute() or not path.is_file() or path.stat().st_size > 2_000_000:
        raise ValueError("UNSAFE_MANIFEST_FILE")
    return path.read_bytes()


def validate(root=ROOT):
    root = Path(root).resolve()
    raw = read(root, "eval/dataset_manifest.json")
    manifest = json.loads(raw)
    version = manifest["dataset_version"]
    if not re.fullmatch(r"mgar-dataset\.v[1-9][0-9]*", version):
        raise ValueError("INVALID_DATASET_VERSION")
    locked = read(root, f"eval/freezes/{version}.sha256").decode().strip()
    if locked != hashlib.sha256(raw).hexdigest():
        raise ValueError("FROZEN_MANIFEST_CHANGED_VERSION_REQUIRED")
    if manifest["schema_version"] != "dataset_manifest.v1" or manifest["oracle_version"] != VERSION:
        raise ValueError("INVALID_DATASET_SCHEMA")
    for path, digest in manifest["files"].items():
        if hashlib.sha256(read(root, path)).hexdigest() != digest:
            raise ValueError("FROZEN_FILE_CHANGED_VERSION_REQUIRED")
    cases = manifest["cases"]
    ids = [c["case_id"] for c in cases]
    if len(set(ids)) != len(ids) or len({c["seed"] for c in cases}) != len(cases):
        raise ValueError("OVERLAPPING_DATASET_SPLITS")
    inputs = json.loads(read(root, manifest["inputs_path"]))
    truth = json.loads(read(root, manifest["truth_path"]))
    if set(inputs) != set(ids) or set(truth) != set(ids):
        raise ValueError("MISSING_OR_EXTRA_TRUTH")
    for split, path in manifest["splits"].items():
        selected = [c for c in cases if c["split"] == split]
        if json.loads(read(root, path)) != selected:
            raise ValueError("CASELIST_DOES_NOT_MATCH_MANIFEST")
    for case in cases:
        cid = case["case_id"]
        if (not re.fullmatch(r"(?:offline|live|development)-[0-9]{3}", cid) or case["dataset_version"] != version
                or case["oracle_version"] != VERSION or case["category"] not in CATEGORIES
                or case["business"] != inputs[cid]["business"] or case["fixture_sha256"] != sha(inputs[cid])
                or truth[cid] != expected(inputs[cid]) or case["negative"] != (truth[cid]["decision"] == "refuse")
                or case["ground_truth_ref"] != manifest["truth_path"] + "#" + cid
                or case["fixture_ref"] != manifest["inputs_path"] + "#" + cid):
            raise ValueError("INDEPENDENT_ORACLE_OR_CASE_MISMATCH")
    offline = [c for c in cases if c["split"] == "offline"]
    live = [c for c in cases if c["split"] == "live"]
    dev = [c for c in cases if c["split"] == "development"]
    if (Counter(c["category"] for c in offline) != {k: 6 for k in CATEGORIES}
            or Counter(c["difficulty"] for c in live) != {"simple": 8, "medium": 8, "hard": 8}
            or sum(c["negative"] for c in live) < 6 or len(dev) < 6
            or any({c["business"] for c in part} != {"incident", "analysis"} for part in [offline, live, dev])):
        raise ValueError("DATASET_COVERAGE_INCOMPLETE")
    # The builder is independent of run outputs and reproduces the frozen seeds.
    from build_dataset import build
    built, generated, answers = build(version)
    for case in built:
        case["fixture_ref"] = manifest["inputs_path"] + "#" + case["case_id"]
        case["ground_truth_ref"] = manifest["truth_path"] + "#" + case["case_id"]
    if canonical(built) != canonical(cases) or generated != inputs or answers != truth:
        raise ValueError("SEED_REPRODUCTION_MISMATCH")
    return manifest


if __name__ == "__main__":
    try:
        manifest = validate()
    except (ValueError, OSError, KeyError, TypeError):
        print(json.dumps({"status": "FAIL", "reason": "dataset freeze invalid; no files repaired"}))
        raise SystemExit(1)
    print(json.dumps({"status": "PASS", "dataset_version": manifest["dataset_version"],
                      "manifest_sha256": hashlib.sha256((ROOT / "eval/dataset_manifest.json").read_bytes()).hexdigest(),
                      "splits": dict(Counter(c["split"] for c in manifest["cases"]))}))
