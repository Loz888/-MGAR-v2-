# E0 engineering load

Status: PASS

| concurrency | assigned | completed | failed | resource_capacity | claim_to_output_p50_ms | claim_to_output_p95_ms | worker_execution_tasks_per_second | including_verification_tasks_per_second | observed_queue_peak | observed_db_connections_peak | observed_db_active_peak | observed_db_ungranted_locks_peak | database_bytes_after |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 4 | 4 | 0 | 4 | 41.384 | 67.557 | 21.420 | 13.443 | 1 | 3 | 2 | 0 | 64025059 |
| 4 | 16 | 16 | 0 | 4 | 72.517 | 142.662 | 43.631 | 29.794 | 4 | 6 | 5 | 2 | 64475619 |
| 8 | 32 | 32 | 0 | 4 | 121.947 | 206.193 | 60.267 | 40.553 | 6 | 10 | 9 | 5 | 65024483 |
| 16 | 64 | 64 | 0 | 4 | 302.537 | 415.091 | 48.370 | 35.023 | 14 | 18 | 17 | 14 | 66318819 |

- Single host only; no power loss, partition, real provider or production traffic.
- Load uses trusted Go Worker processes, fixed SQL/CPU work and local model stub.
- Queue/DB metrics sampled every 10 ms; short peaks may be missed.
- DB resource metrics include other activity in this owned synthetic database.
