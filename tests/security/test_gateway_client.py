from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import threading

import pytest

from mgar_agents.core.gateway import GatewayClient
from mgar_agents.core.tools import PolicyDenied, ToolExecutionError, ToolSpec


@pytest.mark.parametrize("endpoint", ["http://example.org", "http://localhost", "https://u:p@example.org", "https://example.org/tools", "https://example.org?q=1"])
def test_gateway_rejects_unsafe_endpoint(endpoint: str) -> None:
    with pytest.raises(ValueError):
        GatewayClient(endpoint, "a" * 64)


@pytest.mark.parametrize("status", [200, 302, 403, 409])
def test_gateway_transport_no_redirect_retry_or_local_execution(status: int) -> None:
    calls: list[bytes] = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            calls.append(self.rfile.read(int(self.headers["Content-Length"])))
            assert self.headers["Authorization"] == "Bearer " + "a" * 64
            self.send_response(status)
            self.send_header("Location", "/redirect-would-leak-token")
            self.end_headers()
            self.wfile.write(b'{"ok":true}')

        def log_message(self, format: str, *args: object) -> None:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever)
    thread.start()
    try:
        client = GatewayClient(f"http://127.0.0.1:{server.server_port}", "a" * 64)

        def local(*args: object, **kwargs: object) -> None:
            raise AssertionError("local handler bypassed gateway")

        registry = client.registry([ToolSpec("read", {"type": "object", "additionalProperties": False}, local, frozenset({"research"}), "incident.docs")])
        assert "a" * 64 not in registry.declarations_json("research")
        if status == 200:
            assert registry.execute("read", {}, "research", timeout_s=1, max_output_bytes=100) == '{"ok":true}'
        else:
            with pytest.raises(PolicyDenied if status == 403 else ToolExecutionError):
                registry.execute("read", {}, "research", timeout_s=1, max_output_bytes=100)
        assert len(calls) == 1
        assert b'"role"' not in calls[0]
    finally:
        server.shutdown()
        thread.join()
        server.server_close()
