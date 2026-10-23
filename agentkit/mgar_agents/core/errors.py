"""Safe, classified failures shared by SDK and Provider adapters."""


class ProviderFailure(Exception):
    def __init__(self, kind: str, status: str = "failed") -> None:
        self.kind = kind
        self.status = status
        super().__init__(kind + ":" + status)
