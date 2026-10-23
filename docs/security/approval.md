# Approval and Reviewer boundaries

The trusted tool registry classifies readonly effects as read, Action
idempotent_write effects as sandbox_write, and Action non_idempotent effects as
approval_required. Other roles cannot register write capabilities. Approval does
not expand a role, resource, schema, sandbox or network boundary.

A valid non-idempotent call without approval creates a durable pending request
and returns HTTP 409 `approval_required` with `retryable=false`. The response
contains approval_id, action_digest, exact scope and DB-clock expiry. No tool is
reserved or executed. This pauses the particular action, while the task keeps its
lease; pending/approved requests block task output publication. At most 16
approval records may exist per task. The controller must retain the exact call
and renew its lease while awaiting the operator. It must not ask the model to
invent a new call or retry automatically.

An operator reviews that action/scope and invokes:

```text
mgarctl approve APPROVAL_ID --action-digest SHA256
mgarctl approve APPROVAL_ID --action-digest SHA256 --deny
```

The CLI uses the local control-plane database identity as approver; there is no
self-reported approver or Agent-facing approve route. It returns a random one-use
credential only on the first approval. It does not execute the action. Deliver
the credential privately to the trusted dispatcher, outside prompts, tool JSON,
logs and repository files. To resume the retained request, the dispatcher adds
`X-MGAR-Approval-ID` and `X-MGAR-Approval-Token` headers to the original Gateway
call using a currently valid task credential. Existing Python model tool
declarations exclude non-idempotent operations; the privileged resume path is a
controller operation, not a model action.

The persisted binding includes invocation ID, run/task, owner/epoch, plan,
capability/version, resource, canonical argument digest, expiry and approver.
Only a token hash is stored. A task lease renewal can refresh the task credential
without changing that binding; owner takeover or replan cannot reuse it. Expiry
is at most two minutes through the Gateway, bounded by the actual task lease at
request creation. After expiry, a new explicit operator decision is needed for
a new request; the old decision cannot be reopened or extended.

Consumption, tool reservation and their event/outbox entries share one locked
transaction. Concurrent processes cannot consume twice. Changed arguments,
resource, invocation, digest, token or epoch are refused. Denied/expired/consumed
are terminal. Recovery materializes elapsed pending/approved decisions as
expired. Cancellation/terminal workflow and stale task claims remain fenced.
An unknown action outcome blocks replay and task publication. Approval is not an
external exactly-once guarantee, nor an automatic retry policy.

`verifier.AttachReview` accepts only bounded review.v1 data with summary and
registered additional-check IDs. It cannot accept status/passed overrides or
arbitrary executable checks. `Store.RecordReview` loads the actual persisted
verdict under controller authority and appends a digest-only event with safe
check IDs; it neither changes that verdict nor advances workflow success. The
full reviewer prose stays outside default logs. No Reviewer model request or
paid experiment is enabled by this implementation.

Tests use synthetic actions, separate real restricted PostgreSQL credentials,
real HTTP, two OS processes and transactional failure injection. No production
business write or external irreversible operation is exercised.
