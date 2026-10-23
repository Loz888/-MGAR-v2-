"""A fixed, read-only synthetic source index. No model-supplied filesystem paths."""
from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import asdict, dataclass, field
import hashlib
import os
from pathlib import Path, PurePosixPath
import re
import stat
from types import MappingProxyType
from typing import Any

from mgar_agents.core.schemas import canonical_json, strict_json, validate
from mgar_agents.core.tools import PolicyDenied, ToolRegistry, ToolSpec

SOURCE_LIMIT = 16
SOURCE_BYTES = 16384
ID_PATTERN = r"^[A-Za-z][A-Za-z0-9_-]{0,63}$"


@dataclass(frozen=True)
class SourceSpec:
    source_id: str
    relative_path: str
    kind: str
    quality: str
    content_sha256: str
    title: str = ""

    def __post_init__(self) -> None:
        if any(type(value) is not str for value in (self.source_id, self.relative_path, self.kind, self.quality, self.content_sha256, self.title)):
            raise ValueError("source metadata must be strings")
        if not re.fullmatch(ID_PATTERN, self.source_id) or not re.fullmatch(r"[0-9a-f]{64}", self.content_sha256):
            raise ValueError("invalid source identity or content hash")
        parts = self.relative_path.split("/")
        path = PurePosixPath(self.relative_path)
        if not self.relative_path or len(self.relative_path) > 256 or len(parts) > 8 or path.is_absolute() or "\\" in self.relative_path:
            raise ValueError("source path is outside the authorized index")
        if any(not re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}", part) for part in parts) or str(path) != self.relative_path:
            raise ValueError("source path is outside the authorized index")
        if path.suffix not in {".md", ".txt", ".log", ".go"}:
            raise ValueError("unsupported synthetic source type")
        if self.kind not in {"doc", "repo", "log"} or self.quality not in {"reference", "unverified"} or len(self.title) > 128:
            raise ValueError("invalid source classification")


def _read_relative(root: Path, relative: str, max_bytes: int) -> bytes:
    """Open every component without following symlinks; bound regular-file reads.

    Directory descriptors anchor the traversal even if names are replaced while
    it is in progress. This is a local trusted fixture adapter, not a sandbox.
    """
    directory = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    fd: int | None = None
    leaf: int | None = None
    try:
        fd = os.open(root.anchor, directory)
        for component in root.parts[1:]:
            next_fd = os.open(component, directory, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        parts = relative.split("/")
        for component in parts[:-1]:
            next_fd = os.open(component, directory, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        leaf = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
        metadata = os.fstat(leaf)
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1 or metadata.st_size > max_bytes:
            raise PolicyDenied("source must be a bounded regular private fixture file")
        data = bytearray()
        while len(data) <= max_bytes:
            chunk = os.read(leaf, min(4096, max_bytes + 1 - len(data)))
            if not chunk:
                break
            data.extend(chunk)
        if len(data) > max_bytes:
            raise PolicyDenied("source byte bound exceeded")
        return bytes(data)
    except OSError as error:
        raise PolicyDenied("authorized source unavailable or unsafe") from error
    finally:
        if leaf is not None:
            os.close(leaf)
        if fd is not None:
            os.close(fd)


@dataclass(frozen=True, init=False)
class SourceIndex:
    root: Path
    sources: tuple[SourceSpec, ...]
    version: str
    _by_id: Mapping[str, SourceSpec] = field(repr=False)
    _manifest_json: str = field(repr=False)

    def __init__(self, root: Path, specs: Sequence[SourceSpec], version: str) -> None:
        root = Path(root).absolute()
        if root.resolve() != root or not root.is_dir():
            raise ValueError("index root must be a direct local directory")
        if type(version) is not str or not re.fullmatch(r"[A-Za-z0-9_.-]{1,64}", version):
            raise ValueError("invalid index version")
        sources = tuple(specs)
        if not 1 <= len(sources) <= SOURCE_LIMIT or any(not isinstance(source, SourceSpec) for source in sources):
            raise ValueError("invalid source count")
        by_id = {source.source_id: source for source in sources}
        if len(by_id) != len(sources) or len({source.relative_path for source in sources}) != len(sources):
            raise ValueError("duplicate source identity or path")
        manifest = canonical_json({"schema_version": "research_index.v1", "version": version, "sources": [asdict(source) for source in sources]})
        object.__setattr__(self, "root", root)
        object.__setattr__(self, "sources", sources)
        object.__setattr__(self, "version", version)
        object.__setattr__(self, "_by_id", MappingProxyType(by_id))
        object.__setattr__(self, "_manifest_json", manifest)

    @classmethod
    def from_manifest(cls, root: Path) -> SourceIndex:
        root = Path(root).absolute()
        if root.resolve() != root or not root.is_dir():
            raise ValueError("index root must be a direct local directory")
        raw = strict_json(_read_relative(root, "manifest.json", SOURCE_BYTES).decode("utf-8"))
        schema: dict[str, Any] = {
            "type": "object", "additionalProperties": False,
            "required": ["schema_version", "version", "sources"],
            "properties": {"schema_version": {"const": "research_index.v1"}, "version": {"type": "string"},
                "sources": {"type": "array", "minItems": 1, "maxItems": SOURCE_LIMIT,
                    "items": {"type": "object", "additionalProperties": False,
                        "required": ["source_id", "relative_path", "kind", "quality", "content_sha256", "title"],
                        "properties": {name: {"type": "string"} for name in ("source_id", "relative_path", "kind", "quality", "content_sha256", "title")}}}},
        }
        validate(schema, raw)
        return cls(root, [SourceSpec(**item) for item in raw["sources"]], raw["version"])

    @property
    def content_sha256(self) -> str:
        return hashlib.sha256(self._manifest_json.encode()).hexdigest()

    def source(self, source_id: str) -> SourceSpec:
        spec = self._by_id.get(source_id)
        if spec is None:
            raise PolicyDenied("source identity not authorized")
        assert isinstance(spec, SourceSpec)
        return spec

    def _read(self, source_id: str) -> dict[str, Any]:
        spec = self.source(source_id)
        content = _read_relative(self.root, spec.relative_path, SOURCE_BYTES)
        actual = hashlib.sha256(content).hexdigest()
        if actual != spec.content_sha256:
            raise PolicyDenied("source changed from the fixed index")
        try:
            text = content.decode("utf-8")
        except UnicodeDecodeError as error:
            raise PolicyDenied("source is not UTF-8 text") from error
        return {"source_id": spec.source_id, "kind": spec.kind, "quality": spec.quality, "title": spec.title,
                "content_sha256": actual, "byte_length": len(content), "text": text,
                "index_version": self.version, "index_sha256": self.content_sha256}

    def tools(self) -> ToolRegistry:
        def search(arguments: dict[str, Any], *, timeout_s: float) -> dict[str, Any]:
            words = arguments["query"].casefold().split()
            candidates: list[tuple[int, str, dict[str, Any]]] = []
            for source in self.sources:
                observed = self._read(source.source_id)
                text = (observed["title"] + "\n" + observed["text"]).casefold()
                score = sum(word in text for word in words)
                if score:
                    candidate = {key: value for key, value in observed.items() if key not in {"text", "byte_length", "index_version", "index_sha256"}}
                    candidates.append((-score, source.source_id, candidate))
            candidates.sort(key=lambda item: (item[0], item[1]))
            return {"schema_version": "research_search.v1", "query": arguments["query"], "index_version": self.version,
                    "index_sha256": self.content_sha256, "candidates": [item[2] for item in candidates[:arguments.get("limit", 8)]]}

        def read(arguments: dict[str, Any], *, timeout_s: float) -> dict[str, Any]:
            return self._read(arguments["source_id"])

        return ToolRegistry([
            ToolSpec("research.search", {"type": "object", "additionalProperties": False, "required": ["query"],
                "properties": {"query": {"type": "string", "minLength": 1, "maxLength": 256, "pattern": r"\S"},
                    "limit": {"type": "integer", "minimum": 1, "maximum": 8}}}, search,
                roles=frozenset({"research"}), scope="synthetic-index:" + self.content_sha256),
            ToolSpec("research.read", {"type": "object", "additionalProperties": False, "required": ["source_id"],
                "properties": {"source_id": {"enum": list(self._by_id)}}}, read,
                roles=frozenset({"research"}), scope="synthetic-index:" + self.content_sha256,
                max_output_bytes=32768),
        ])
