// Package telemetry turns what the exchange measures into Prometheus metrics
// (week 7, ADR-0011). It is the ONLY package that imports the Prometheus
// client; every other package reports through a small interface or a plain
// function it declares itself:
//
//	engine.Observer         queue wait, durable append, apply, total, refusals
//	journal OnSync func     bytes, write time, fsync time
//	marketdata Hub.Stats    subscribers, deliveries, slow-consumer drops
//	projector.Status        projected Seq, health, failures
//	api Server.Route        the route pattern used as the HTTP label
//
// So the core stays free of third-party code (ADR-0003 rule 4), and every
// package can be tested with a tiny fake instead of a metrics registry.
//
// # The three latencies the week-7 report must keep apart
//
//	HTTP   cex_http_request_duration_seconds   handler start → handler return
//	  └─ engine total  cex_engine_command_seconds   admission → reply
//	        ├─ queue   cex_engine_queue_wait_seconds
//	        ├─ durable cex_engine_durable_seconds   (journal write + fsync, per batch)
//	        └─ apply   cex_engine_apply_seconds     (pure engine time)
//
// Each level contains the one below, so the gap between two levels is
// itself a finding: HTTP minus engine total is JSON, auth and scheduling;
// engine total minus apply is waiting.
//
// # Saturation and errors
//
// cex_engine_queue_length against cex_engine_queue_capacity shows how full
// the engine is; cex_engine_refused_total{reason="overloaded"} counts what
// was shed. Business rejections are not errors: they are counted by
// outcome in cex_engine_commands_total. Server errors are the 5xx codes of
// cex_http_request_duration_seconds_count.
//
// All metrics are served on the private admin listener (CEX_ADMIN_ADDR),
// never on the public API port.
package telemetry
