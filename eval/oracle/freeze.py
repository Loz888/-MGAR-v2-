"""Developer-only freeze creation. Existing version locks are never overwritten."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re

from build_dataset import build
from reference import VERSION

ROOT = Path(__file__).resolve().parents[2]


def serialized(value):
    return (json.dumps(value, indent=2, ensure_ascii=False) + "\n").encode()


def freeze(root, version):
    root = Path(root).resolve()
    if not re.fullmatch(r"mgar-dataset\.v[1-9][0-9]*", version):
        raise ValueError("INVALID_DATASET_VERSION")
    lock = root / f"eval/freezes/{version}.sha256"
    manifest_path = root / "eval/dataset_manifest.json"
    if lock.exists() or lock.is_symlink():
        raise ValueError("FROZEN_VERSION_EXISTS_INCREMENT_VERSION")
    if manifest_path.exists():
        previous = json.loads(manifest_path.read_text())
        previous_version = previous["dataset_version"]
        old_lock = root / f"eval/freezes/{previous_version}.sha256"
        if (old_lock.read_text().strip() != hashlib.sha256(manifest_path.read_bytes()).hexdigest()
                or int(version.rsplit("v", 1)[1]) <= int(previous_version.rsplit("v", 1)[1])):
            raise ValueError("INVALID_PREVIOUS_FREEZE_OR_VERSION")
    tag = version.rsplit(".", 1)[1]
    inputs_path = f"fixtures/datasets/{tag}/inputs.json"
    truth_path = f"eval/oracle/{tag}_truth.json"
    if (root / inputs_path).exists() or (root / truth_path).exists():
        raise ValueError("EXISTING_DATASET_FILES_NOT_OVERWRITTEN")
    cases, inputs, truth = build(version)
    for case in cases:
        case["fixture_ref"] = inputs_path + "#" + case["case_id"]
        case["ground_truth_ref"] = truth_path + "#" + case["case_id"]
    splits = {"offline": "eval/cases/offline-60.json", "live": "eval/cases/frozen-live-24.json", "development": "eval/cases/development.json"}
    contents = {inputs_path: inputs, truth_path: truth}
    contents.update({path: [c for c in cases if c["split"] == split] for split, path in splits.items()})
    for name, content in contents.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        if path.is_symlink():
            raise ValueError("SYMLINK_FREEZE_TARGET")
        path.write_bytes(serialized(content))
    files = {name: hashlib.sha256((root / name).read_bytes()).hexdigest() for name in contents}
    for name in ["reference.py", "build_dataset.py", "validate_manifest.py", "seed.py", "freeze.py"]:
        path = "eval/oracle/" + name
        files[path] = hashlib.sha256((root / path).read_bytes()).hexdigest()
    for path in sorted((root / "fixtures/incident").rglob("*")):
        if path.is_file():
            files[str(path.relative_to(root))] = hashlib.sha256(path.read_bytes()).hexdigest()
    manifest = {"schema_version": "dataset_manifest.v1", "dataset_version": version, "oracle_version": VERSION,
                "inputs_path": inputs_path, "truth_path": truth_path, "splits": splits,
                "files": files, "cases": cases,
                "rules": ["No model output is a ground-truth source.", "Change data or oracle: increment dataset version and rerun every arm.",
                          "Live holdout is not prompt-development data.", "Semantic oracle pass is not proof of runtime execution."]}
    raw = serialized(manifest)
    manifest_path.write_bytes(raw)
    lock.parent.mkdir(parents=True, exist_ok=True)
    (lock.parent / f"{version}.manifest.json").write_bytes(raw)
    with lock.open("x") as out:
        out.write(hashlib.sha256(raw).hexdigest() + "\n")
    return hashlib.sha256(raw).hexdigest()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    try:
        digest = freeze(ROOT, args.version)
    except (ValueError, OSError, KeyError):
        print(json.dumps({"status": "FAIL", "reason": "freeze refused; use a new version with reviewed source inputs"}))
        raise SystemExit(1)
    print(json.dumps({"status": "FROZEN", "version": args.version, "manifest_sha256": digest}))
