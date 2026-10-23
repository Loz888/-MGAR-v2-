"""Local synthetic research role. No network, shell, or authoritative DB writes."""
from .agent import ResearchAgent
from .index import SourceIndex, SourceSpec

__all__ = ["ResearchAgent", "SourceIndex", "SourceSpec"]
