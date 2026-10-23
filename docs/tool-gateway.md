# Tool Gateway v1

`policy.NewGateway` is an HTTP handler for `POST /v1/tools`. The trusted dispatcher
installs immutable tool definitions and calls `Issue` after a real executing task
claim check. There is no public issue/claim endpoint. A credential is a random
256-bit bearer, scoped to the exact run/task/owner/epoch/revision/plan and an exact
resource allowlist. Only its hash is held by the server; restarts revoke tokens.
Worker launch flags, prompt contents and request JSON cannot alter role.

Call fields: schema=`tool-call.v1`, request_id=UUID, tool, version, resource,
arguments=closed JSON object. Every argument is required; integers are bounded,
strings use registered enum choices, booleans are typed. Null, duplicate/unknown
keys, arbitrary SQL/shell/path and unknown versions are denied. Resource IDs map
to controller-owned resources; handlers never interpret them as arbitrary paths.

The service persists `tool_invocations` reservation and event/outbox before
execution, holding no DB transaction during a handler. Success links a bounded
CAS `tool_result` artifact to the fixed task snapshot and writes settlement plus
outbox atomically. A repeated request ID must match its canonical request hash
and identity; it can return the same stored success. Reserved/unknown/failed
requests cannot authorize another physical execution. There is a conservative
64-call cap per task. Pending/unknown tools prevent task output publication.
Non-idempotent tools are denied pending the P5-05 approval implementation.

Authenticated denials append digest-only DB audit events even after lease expiry;
unauthenticated requests use the mandatory structured audit sink without trusting
any submitted run/task identity. Sink implementations must persist those entries.
No token, raw arguments, prompt, or handler error is logged. A DB audit failure
fails closed. Internet deployment needs TLS, request/body deadlines and ingress
rate limits; this card exercises a private loopback HTTP service.

Python `GatewayClient.registry` replaces every supplied local tool handler with
this remote API. The P2 fixture registry remains an explicitly trusted offline
fixture adapter. The existing recovery-only daemon does not yet dispatch workers;
its end-to-end composition belongs to P7. The gateway and SDK are executable
interfaces, tested over real HTTP plus synthetic PostgreSQL. Credentials must be
supplied privately by the dispatcher, outside model context/tool declarations.

Only trusted bounded adapters may be installed. P5-04 supplies process isolation
for untrusted Action/Verifier commands; a Go callback alone cannot forcibly stop
arbitrary malicious code. A callback timeout/error is conservatively unknown.
