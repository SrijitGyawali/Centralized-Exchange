# evidence/

Proof that the claims in the README hold. Every file here names the git
commit or release it came from.

- `bench/`: raw `go test -bench -benchmem` output
- `load/`: load-test runs from `cmd/loadgen`, one directory per run, each
  with `report.md`, `results.json` (workload, environment, server settings
  read from its metrics), `requests.csv.gz` (one row per arrival),
  `timeline.csv` (per-second server state) and `metrics-end.prom`. The
  week-7 report that interprets them is [docs/PERFORMANCE.md](../docs/PERFORMANCE.md).
- `profiles/`: CPU/alloc profiles and execution traces, with a note on what
  they showed and what was changed because of it
- `crash/`: crash-drill reports
- `demo/`: recordings and the 3-minute demo script

Never edit a result by hand. Re-run and commit the new output. A run made
from uncommitted code says `-dirty` in its git commit and does not belong
here.
