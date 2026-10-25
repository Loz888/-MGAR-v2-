# E0 engineering load

Status: PASS

| concurrency | assigned | completed | failed | resource_capacity | claim_to_output_p50_ms | claim_to_output_p95_ms | worker_execution_tasks_per_second | including_verification_tasks_per_second | observed_queue_peak | observed_db_connections_peak | observed_db_active_peak | observed_db_ungranted_locks_peak | database_bytes_after |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 4 | 4 | 0 | 4 | 45.941 | 57.450 | 20.732 | 13.717 | 1 | 3 | 2 | 0 | 62124515 |
| 4 | 16 | 16 | 0 | 4 | 59.427 | 116.169 | 52.635 | 32.346 | 2 | 6 | 5 | 1 | 62853603 |
| 8 | 32 | 32 | 0 | 4 | 102.601 | 201.124 | 64.665 | 41.111 | 5 | 10 | 8 | 6 | 63738339 |
| 16 | 64 | 64 | 0 | 4 | 293.465 | 568.046 | 46.073 | 33.540 | 14 | 18 | 17 | 14 | 65237475 |

- Single host only; no power loss, partition, real provider or production traffic.
- Load uses trusted Go Worker processes, fixed SQL/CPU work and local model stub.
- Queue/DB metrics sampled every 10 ms; short peaks may be missed.
- DB resource metrics include other activity in this owned synthetic database.
