# loadtest/

Reproducible workload definitions (k6 scripts and/or a Go load generator).
Built in week 7.

Every run must record: hardware, OS, storage, Go version, git commit,
CPU and memory limits, sync policy, book depth, account count and order
mix. Use an **open arrival-rate** workload (a fixed offered rate) and
report dropped iterations, error counts and p50/p95/p99. Raw output goes
to `evidence/`.

Targets such as "p99 < 50 ms at 100 orders/s" are hypotheses to test, not
results.
