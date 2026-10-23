"""Credential-bound remote tools. Only the trusted dispatcher supplies the token.

No PostgreSQL credentials or self-reported role is sent. Transport errors and
unknown execution results are never automatically retried or redirected.
"""
from __future__ import annotations

from dataclasses import replace
import http.client
import ipaddress
import math
import re
import ssl
from typing import Any
from urllib.parse import urlsplit
from uuid import uuid4

from .schemas import canonical_json, strict_json
from .tools import PolicyDenied, ToolExecutionError, ToolRegistry, ToolSpec


class GatewayClient:
    def __init__(self, endpoint: str, token: str) -> None:
        parsed = urlsplit(endpoint)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in {"", "/"}:
            raise ValueError("invalid gateway endpoint")
        if parsed.scheme == "http":
            try:
                local = ipaddress.ip_address(parsed.hostname).is_loopback
            except ValueError:
                local = False
            if not local:
                raise ValueError("plaintext gateway requires a loopback IP")
        if not re.fullmatch(r"[0-9a-f]{64}", token):
            raise ValueError("invalid gateway credential")
        self._endpoint = parsed
        self._token = token

    def call(self, tool: str, version: str, resource: str, arguments: dict[str, Any], *, timeout_s: float, request_id: str | None = None) -> dict[str, Any]:
        if not math.isfinite(timeout_s) or not 0 < timeout_s <= 30:
            raise PolicyDenied("invalid deadline")
        body = canonical_json({"schema": "tool-call.v1", "request_id": request_id or str(uuid4()), "tool": tool, "version": version, "resource": resource, "arguments": arguments})
        if len(body.encode()) > 16384:
            raise PolicyDenied("request too large")
        host = self._endpoint.hostname
        assert host is not None
        conn: http.client.HTTPConnection
        if self._endpoint.scheme == "https":
            conn = http.client.HTTPSConnection(host, self._endpoint.port, timeout=timeout_s, context=ssl.create_default_context())
        else:
            conn = http.client.HTTPConnection(host, self._endpoint.port, timeout=timeout_s)
        try:
            conn.request("POST", "/v1/tools", body.encode(), {"Authorization": "Bearer " + self._token, "Content-Type": "application/json"})
            response = conn.getresponse()
            raw = response.read(65537)
            if response.status == 403:
                raise PolicyDenied("server denied tool capability")
            if response.status != 200 or len(raw) > 65536:
                raise ToolExecutionError("unconfirmed tool result; do not retry")
            return strict_json(raw.decode("utf-8"))
        except (PolicyDenied, ToolExecutionError):
            raise
        except (OSError, http.client.HTTPException, ValueError) as error:
            raise ToolExecutionError("unconfirmed tool transport; do not retry") from error
        finally:
            conn.close()

    def registry(self, declarations: list[ToolSpec]) -> ToolRegistry:
        """Replace every local handler, retaining declarations for model guidance.

        Local role/schema checks improve errors; the server owns authorization.
        """
        def remote(spec: ToolSpec) -> ToolSpec:
            def invoke(arguments: dict[str, Any], *, timeout_s: float) -> dict[str, Any]:
                return self.call(spec.name, spec.schema_version, spec.scope, arguments, timeout_s=timeout_s)
            return replace(spec, handler=invoke)
        return ToolRegistry([remote(spec) for spec in declarations])
