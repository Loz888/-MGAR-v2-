"""Deterministic offline test provider. No remote provider is enabled."""


class MockProvider:
    def complete(self, prompt: str) -> str:
        return "mock:" + prompt


def provider(name: str = "mock") -> MockProvider:
    if name != "mock":
        raise PermissionError("real provider disabled; no network request performed")
    return MockProvider()
