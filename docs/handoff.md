# Durable Handoff

Version: handoff.v1

P4-01 uses the existing outbox/inbox tables; migrations and historical events remain unchanged.
New outbox payloads carry `handoff.v1`, Run, active Plan Version and entity Revision.
Workflow audit payloads retain their existing format. Legacy outbox payloads are consumed
as stale hints; the independent authoritative recovery scan still reconstructs ready work.

`handoff.Dispatcher` scans a bounded batch outside transactions. It sends each event and
then marks publication. The per-consumer scan includes published events whose receipt
is absent or only received. Publication therefore cannot suppress a lost notification.
Transport can duplicate; external exactly-once execution is not promised.

`Store.ConsumeHandoff` accepts only a stored event ID and the trusted scheduler consumer.
It validates Run, Orchestrator ownership/epoch/revision/lease, event plan and entity
revision. Messages never supply Task state, input authorization or verification status.
Under the existing Workflow→Orchestrator→Budget→Task→Artifact lock order, authoritative
ready/input updates and the processed receipt commit together. A received receipt resumes;
a processed receipt returns the persisted acknowledgement. Stale events are acknowledged
without unlocking work. A later budget release or verdict is handled by a newer event
or the independent state scan; processing a hint does not claim Worker execution.

Recovery dispatches a bounded batch after reading verdicts, then performs its existing
state scan and finalization. The current transport is an in-process trusted callback;
independent consumer processes and database transactions are exercised by integration tests.
No broker, paid Provider, automatic Python Worker or network authentication boundary is added.

H01–H04 tests kill only owned child sessions with SIGKILL, at actual transaction/transport
barriers. H01 and H04 use PostgreSQL trigger locks in disposable test schemas; H03 stops
the actual Dispatcher after consumer commit and before publication. Tests retain H05 old
plan, cross-Run, wrong-owner, rollback, lost notification and four-process duplicate checks.
