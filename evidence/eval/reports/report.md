# E0 Mock engineering experiment analysis

All assigned cases and failures retained. N/A means missing or inapplicable; never zero-filled.

| Arm | N | Oracle pass | Rate | Unsupported answer rate | False Workflow success rate | Input tokens | Output tokens | Usage coverage | Cost USD | Experiment P50 ms | P95 ms | Critical retention | Role coverage |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| B0 | 60 | 36 | 0.6000 | 0.3333 | N/A | 94662.0000 | 4446.0000 | 1.0000 | 0.0000 | 382.7226 | 15413.7506 | N/A | N/A |
| B1 | 60 | 36 | 0.6000 | 0.3333 | N/A | 157142.0000 | 23322.0000 | 1.0000 | 0.0000 | 590.1112 | 15905.7564 | 1.0000 | 1.0000 |
| B2 | 60 | 48 | 0.8000 | 0.0000 | N/A | 186326.0000 | 37215.0000 | 1.0000 | 0.0000 | 965.8161 | 15958.1879 | 1.0000 | 1.0000 |
| B3 | 60 | 48 | 0.8000 | 0.0000 | N/A | 186734.0000 | 37215.0000 | 1.0000 | 0.0000 | 953.8175 | 16398.2857 | 1.0000 | 1.0000 |
| B4 | 60 | 48 | 0.8000 | 0.0000 | 0.0000 | 233946.0000 | 55506.0000 | 1.0000 | 0.0000 | 1172.3206 | 24077.2153 | N/A | 1.0000 |
| A1 | 60 | 36 | 0.6000 | 0.0000 | 0.0000 | 169098.0000 | 40170.0000 | 1.0000 | 0.0000 | 998.6986 | 23347.2449 | N/A | 1.0000 |
| A2 | 60 | 48 | 0.8000 | 0.0000 | N/A | 186734.0000 | 37215.0000 | 1.0000 | 0.0000 | 942.0532 | 16369.0837 | 1.0000 | 1.0000 |

## Paired Oracle differences (right minus left)

| Comparison | Pairs | Delta | 95% CI |
|---|---:|---:|---|
| B1 − B0 | 60 | 0.0000 | [0.0, 0.0] |
| B2 − B1 | 60 | 0.2000 | [0.1, 0.3] |
| B3 − B2 | 60 | 0.0000 | [0.0, 0.0] |
| B4 − B3 | 60 | 0.0000 | [0.0, 0.0] |
| B4 − A1 | 60 | 0.2000 | [0.1, 0.3] |
| B4 − A2 | 60 | 0.0000 | [0.0, 0.0] |

## Limits

- Mock decisions are reviewed templates; no real model ability claim.
- One run per case; sequential nonrandomized order; descriptive latency only.
- Bootstrap is conditional on frozen cases, not model sampling uncertainty.
- Tokens are local_stub UTF-8 bytes/4, cost uses demo-free.v1.
- Strict selector records unavailable: retention N/A. Citation validity is identity, not semantic correctness.
- B3 and A2 are independent runs of the same treatment.

Full categories, per-case values, usage bounds and Workflow latency coverage are in the adjacent CSV/JSON files.
Input index SHA256: `ff174a9ea49891ab9ed08bd5243e380dda2ef9c126e62efa787dc833fd007b19`.
