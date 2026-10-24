# Resource admission and backpressure

P6-02 adds database-wide pools for llm (default 2), cpu (2), and db (4), each with
queue_limit=64. A single Pool row lock controls admission across workflows and
processes; process-local semaphores add a second bound. Configure before creating
resource managers. A later capacity increase may leave older local managers
conservatively smaller, while the DB limit always remains authoritative.

A request requires an actual executing task claim, class, priority 0..10 and a
maximum 30-second total deadline. Workflow/claim/plan/lease checks precede queue
or execution admission. FIFO tie-breaking and 100 ms priority aging prevent new
high-priority work from permanently overtaking older requests. Queue depth is
bounded, obsolete/deadline-expired queue entries are ineligible, and waiting ends
with an explicit error. Under no unknown occupancies and bounded service time T,
waiting is bounded by priority catch-up (at most 1.1 s), one existing service
window T, and ceil(queue_limit/capacity)*T for queued work ahead; a shorter request deadline instead
fails. This is a conditional bound, not an unconditional fairness promise.

`Resources.Run` waits on the authoritative queue, enters the local semaphore,
monitors task authority and propagates cancellation to the trusted executor.
Only a confirmed stopped executor releases the durable permit. Queued cancellation
releases without executing; unconfirmed stop remains unknown and still occupies
capacity. Merely passing a deadline does not prove a crashed worker's child or
remote request stopped. Such cases require controller/operator confirmation;
there is no unsafe automatic permit recycling. `ConfirmResourceStopped` lets a
live controller record an independently obtained stop-proof digest and release
a held/unknown ticket even if its private token was lost. It is a trusted
attestation API; it does not perform cleanup or infer stop from elapsed time.
Private ticket tokens stay on the
host and are excluded from JSON/audit payloads.

Gateway uses CPU for Action/Verifier tools and DB for bounded reads; its timeout
includes queue and execution. A queue failure before executing settles the tool
as a known failed attempt; later uncertain tool effects retain the P6-01 budget
bound independently of resource stop confirmation. The Docker adapter reports
actual container removal so normal container timeout releases CPU capacity.
`Resources.Model` combines LLM admission with durable budget reservation and
settlement; mock-only budget policy still rejects paid providers.

All resource transitions and configuration changes append Workflow event/outbox
in the same transaction. Lock order is Workflow → optional Orchestrator → Budget
→ Task → Pool → Ticket → outbox. Choosing the global queue head reads other runs
without reverse-locking their workflows. Readiness and artifact authorization are
rechecked by the existing task guard. Existing read-only Verifier fixture APIs
remain callable by the trusted controller; production dispatch must wrap such
work in Resources.Run, as demonstrated for Gateway and model adapters. Full
Worker/controller assembly remains in P7.

Tests cover three simultaneous classes, cross-workflow/two-process last-permit
competition, overflow/deadline, aging, fencing, token forgery, rollback, cancellation
acknowledgement, retained unknown capacity and a real timed-out Docker tool whose
confirmed removal releases its CPU permit. No untrusted host payload runs.
