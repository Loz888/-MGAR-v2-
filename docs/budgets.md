# Workflow budget accounting

P6-01 extends the existing run_budgets row; migration 005 is additive and
backfills historical tool records. Model calls, tool calls, tokens, estimated
micro-dollars, task delegations and concurrency are bounded for the whole run.
Only the live controller can configure limits; lowering below committed usage
is rejected. Model/tool reservation locks the same budget row, applies a
conditional update, records the ledger and appends audit/outbox atomically before
any physical call. Repeated cached tool success consumes no new reservation.

`reserved` is pending, `succeeded|failed` is settled, and `unknown` retains its
reservation. Known success or failure consumes one attempt, refunds unused
estimated token/cost bounds and releases call concurrency. Unknown never refunds
unobserved amounts, never records a zero usage object and never authorizes replay.
Shared call concurrency is deliberately conservative: unknown entries still hold
capacity. Per-resource admission belongs to P6-02. Task attempts/lease takeover
retain the existing P1 delegation accounting semantics.

Frozen pricing records include version, Provider, Model, integer micro-dollars
per million input/output tokens and separate input/output bounds. Quotes and
settlement use those immutable values with ceiling division, not floats or a
current mutable price registry. Old manually supplied bounds are labeled
legacy_bound.v1. This card uses synthetic mock prices only; it enables no paid
provider and makes no real invoice claim. Input digests are provenance, never a
global deduplication key; logical IDs and task identities define calls.

`BudgetReport` reads a consistent repeatable-read snapshot and independently sums
ledger entries, checks usage-known flags, task counts and every aggregate. A
mismatch is reported as conserved=false, without repair or deletion. Costs are
integer micro-dollars; reserved estimates must be shown separately from settled
usage, with unknown call counts. Conservative retained reservations can require
an explicit controller/operator resolution; no automatic zero settlement exists.

The acceptance suite includes 20 independent OS processes competing for model,
tool and mixed concurrency quotas; fixed identical input digests across tasks;
old-schema upgrade with retained historical tool usage; distinct price versions;
known failure/refund, unknown retention, duplicate settlement, counter drift and
outbox rollback. It uses only disposable synthetic PostgreSQL schemas.
