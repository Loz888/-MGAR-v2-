"""Provider adapters. Real network admission is disabled without trusted policy."""
from ..core.provider import MockProvider as StructuredMockProvider
from .deepseek import CallLedger, DeepSeekProvider, ProviderConfig, Reservation, Usage

__all__ = ["MockProvider", "StructuredMockProvider", "provider", "CallLedger", "DeepSeekProvider", "ProviderConfig", "Reservation", "Usage"]


class MockProvider:
    """Compatibility text fixture; new Agent loops use StructuredMockProvider."""
    def complete(self, prompt: str) -> str:
        return "mock:" + prompt


def provider(name: str = "mock") -> MockProvider:
    # P6 must supply a trusted durable admission/settlement implementation before
    # a production factory can construct a chargeable Provider.
    if name != "mock":
        raise PermissionError("real provider disabled; no network request performed")
    return MockProvider()
