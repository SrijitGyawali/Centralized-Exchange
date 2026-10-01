# deploy/

Packaging and local deployment. Built in week 7.

- `Dockerfile`: multi-stage build that produces a static `cexd` binary
  in a minimal image, running as a non-root user.
- `docker-compose.yml`: `cexd` + PostgreSQL + Prometheus/Grafana, with a
  **named persistent volume** for `data/` (the WAL).

Note: a container volume on Docker Desktop for Windows does not give the
same durability guarantees as bare-metal Linux. Crash tests must record
the actual filesystem used.
