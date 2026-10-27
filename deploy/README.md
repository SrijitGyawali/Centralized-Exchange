# deploy/

Packaging and the local stack (week 7). One command starts everything:

```bash
make up      # docker compose -f deploy/docker-compose.yml up -d --build --wait
make down    # stop; named volumes (journal, database, metrics) are kept
```

| URL | What |
|---|---|
| http://localhost:8080 | the exchange: web UI, `/v1` API, `/stream/book` |
| http://localhost:6060/metrics | cexd's private admin port: metrics, `/debug/pprof/` |
| http://localhost:9090 | Prometheus: targets, alert rules, ad-hoc PromQL |
| http://localhost:3000 | Grafana: four provisioned dashboards (anonymous viewer) |

Every port is published on **127.0.0.1 only**. Passwords and tokens in this
directory are local demo values published in the repository: never reuse
them.

## Files

| File | Purpose |
|---|---|
| [`Dockerfile`](Dockerfile) | Multi-stage build: `golang:1.26.10-alpine` compiles a static binary (`CGO_ENABLED=0`); the runtime image is `distroless/static:nonroot` (no shell), user 65532, `HEALTHCHECK` via `cexd healthcheck`. |
| [`docker-compose.yml`](docker-compose.yml) | `postgres`, `cexd`, `prometheus`, `grafana`; named volumes `cex-data` (the WAL), `cex-readmodel`, `prometheus-data`, `grafana-data`. |
| [`cexd.env`](cexd.env) | The container's `CEX_API_TOKENS`: the load generator's accounts (kept in sync by a test). Admin token `loadgen-local-admin-0001`. |
| [`prometheus/prometheus.yml`](prometheus/prometheus.yml) | Scrapes `cexd:6060` every 5 s. |
| [`prometheus/alerts.yml`](prometheus/alerts.yml) | Engine halted, scrape down, queue nearly full, shedding, 5xx ratio, order p99 budget, projector unhealthy, projection lag, slow consumers. Each names the operator action. |
| [`grafana/provisioning/`](grafana/provisioning/) | Creates the Prometheus data source and loads the dashboards at startup. |
| [`grafana/dashboards/`](grafana/dashboards/) | 1 trading engine, 2 HTTP API, 3 read model and market data, 4 Go runtime, disk and recovery. Dashboards are code: edit the JSON, not the UI. |

## Notes

- **The WAL is the exchange.** It lives in the named volume `cex-data`,
  which survives `make down` and image rebuilds. `docker compose down -v`
  deletes it, and with it every balance and order.
- **Durability inside Docker Desktop** is not bare-metal Linux: the volume
  sits on a virtual disk in a VM. fsync there measured ~4 ms against ~1.2 ms
  on the host's NVMe (see `docs/PERFORMANCE.md`). Crash tests must record
  the actual filesystem used.
- **Graceful stop.** `docker stop` sends SIGTERM; cexd stops HTTP, drains
  the engine and closes the journal. `stop_grace_period` (20 s) exceeds
  `CEX_SHUTDOWN_TIMEOUT` (10 s) so a drain is never cut off by SIGKILL.
- **Only PostgreSQL** (to run cexd on the host instead): `make db-up`, then
  `CEX_DATABASE_URL=postgres://cex:cex-local-only@127.0.0.1:5433/cex make run`.
- The Compose project is named `cex`, so containers are `cex-cexd-1`,
  `cex-postgres-1`, … (before week 7 the project took the directory's name,
  `deploy`; an old `deploy_cex-readmodel` volume can be removed).
