# Synthetic release 2026-10-20
Change R-17 moved aggregation into aggregate.go: duplicate order_events join rows now reach the aggregation loop.
The prior deduplication by order_id and exclusion of pending orders were removed before totals were summed.
Synthetic orders 101 (paid 100, emitted twice), 102 (paid 50), and 103 (pending 70) produce a naive total 320; the deduplicated paid total is 150.
This document is a planted clue for the synthetic incident, not a verified production diagnosis.
