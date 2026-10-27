// Command cexd is the exchange server.
//
// This file is the composition root: the one place that reads the
// environment, constructs every component and wires them together. It
// contains no business logic. run() goes through these steps:
//
//  1. load config
//  2. recover: open the journal in DataDir, replay it into a fresh state,
//     check every invariant, start the engine (node.Open)
//  3. (week 6) start projector and market data
//  4. (week 7) metrics everywhere, served with pprof on a private admin port
//  5. serve HTTP; /readyz answers 200 once recovery has succeeded
//  6. on SIGINT/SIGTERM shut down in reverse order: stop HTTP, then stop
//     admitting, drain the engine and close the journal
//
// `cexd healthcheck` instead asks a running server's /readyz and exits 0 or
// 1 (for the container HEALTHCHECK; see healthcheck.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/api"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/config"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/marketdata"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/node"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/projector"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/telemetry"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/webui"
)

// version is overwritten at build time:
//
//	go build -ldflags "-X main.version=v0.1.0" ./cmd/cexd
var version = "dev"

// main stays tiny. All the work happens in run, which returns an error
// instead of calling os.Exit or log.Fatal. Deferred cleanup therefore always
// runs, and tests can call run directly.
func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := healthcheck(os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(context.Background(), os.Getenv, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "cexd: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	// ctx is canceled on Ctrl-C (SIGINT) or SIGTERM (what Docker and
	// Kubernetes send). Everything long-running watches this context.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}

	// Structured JSON logs: machine-parseable key/value pairs instead of
	// formatted strings.
	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	logger.Info("starting cexd",
		"version", version,
		"http_addr", cfg.HTTPAddr,
		"data_dir", cfg.DataDir,
	)

	// Metrics (week 7) exist before recovery, so the engine and the journal
	// report from their very first command.
	metrics := telemetry.New(version)

	// Recovery: replay the journal into a fresh state and start the engine.
	// If the journal is corrupt or the recovered state breaks an invariant,
	// the server refuses to start rather than trade on a wrong state.
	ecfg := engine.DefaultConfig()
	ecfg.BatchSize = cfg.BatchSize
	ecfg.QueueSize = cfg.QueueSize
	ecfg.Observer = metrics.Engine()
	nd, err := node.Open(node.Config{Dir: cfg.DataDir, Engine: ecfg, JournalOnSync: metrics.JournalOnSync})
	if err != nil {
		return fmt.Errorf("recovery: %w", err)
	}
	// Deferred calls run in reverse order of registration. This one is
	// registered BEFORE the HTTP server exists, so it runs AFTER the server
	// has shut down: no request can arrive while the engine drains.
	defer func() {
		if err := nd.Close(); err != nil {
			logger.Error("closing the node", "err", err)
			return
		}
		// After the drain every applied command is durable, so this one Seq
		// is both the applied and the durable sequence at exit.
		logger.Info("engine drained and journal closed", "applied_seq", nd.Engine.Snapshot().Seq)
	}()
	logger.Info("recovered",
		"commands", nd.Recovery.Commands,
		"last_seq", nd.Recovery.LastSeq,
		"truncated_bytes", nd.Recovery.TruncatedBytes,
		"digest", fmt.Sprintf("%x", nd.Recovery.Digest),
		"took", nd.Recovery.Took.String(),
	)
	metrics.RecordRecovery(nd.Recovery)
	metrics.WatchEngine(nd.Engine)
	metrics.WatchJournalFile(filepath.Join(cfg.DataDir, journal.FileName))
	appliedSeq := func() domain.Seq { return nd.Engine.Snapshot().Seq }

	// The SQL read model (week 6) is optional. When enabled, a projector
	// goroutine follows the journal file and writes PostgreSQL. It never
	// talks to the engine, and trading never waits for it: a database outage
	// only makes history stale (ADR-0009). The URL holds a password, so it is
	// never logged.
	var projection api.ProjectionStatus
	// watched must stay a NIL INTERFACE when the read model is off. Assigning
	// a nil *projector.Status to it would make a non-nil interface holding a
	// nil pointer (the "typed nil" trap), and WatchProjection's nil check
	// would pass, then crash on the first scrape.
	var watched telemetry.Projection
	if cfg.DatabaseURL != "" {
		pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL) // connects lazily; never blocks startup
		if err != nil {
			return fmt.Errorf("database: invalid CEX_DATABASE_URL")
		}
		status := &projector.Status{}
		projCtx, stopProjector := context.WithCancel(context.Background())
		projDone := make(chan struct{})
		go func() {
			defer close(projDone)
			projector.Run(projCtx, projector.Config{
				JournalPath: filepath.Join(cfg.DataDir, journal.FileName),
				Pool:        pool,
			}, 0, status, logger)
		}()
		// Registered AFTER the node's deferred Close, so it runs BEFORE it:
		// the projector stops first, then the engine drains.
		defer func() {
			stopProjector()
			<-projDone
			pool.Close()
			logger.Info("projector stopped", "projected_seq", status.ProjectedSeq())
		}()
		projection = status
		watched = status
		logger.Info("projector enabled")
	}
	metrics.WatchProjection(watched, appliedSeq)

	tokens := make(map[string]api.Principal, len(cfg.Tokens))
	for token, identity := range cfg.Tokens {
		tokens[token] = api.Principal{User: identity.User, Admin: identity.Admin}
	}
	// Public market data (week 6): a hub reads the engine's lock-free
	// snapshot at a bounded rate and fans it out over WebSocket. It never
	// touches the engine goroutine, so no viewer can slow matching down.
	hub := marketdata.New(nd.Engine.Snapshot, marketdata.Config{})
	metrics.WatchHub(hub.Stats)
	hubCtx, stopHub := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); hub.Run(hubCtx) }()
	defer func() { stopHub(); <-hubDone }()

	// One server, three kinds of route:
	//   /stream/book  public WebSocket feed (no token, no private data)
	//   /, /ui/...    the web client (static files)
	//   everything else: health checks and the authenticated /v1 API
	apiServer := api.NewServer(nd.Engine, nd.Ready, api.Config{
		Tokens: tokens, RatePerSecond: cfg.RatePerSecond, Burst: cfg.RateBurst,
		RequestTimeout: cfg.RequestTimeout,
		Projection:     projection,
	})
	routes := http.NewServeMux()
	routes.Handle("GET /stream/book", hub.Handler())
	routes.Handle("GET /{$}", webui.Handler()) // {$}: exactly "/", not every path
	routes.Handle("GET /ui/", webui.Handler())
	routes.Handle("/", apiServer)

	// Every request is timed by route, except the stream: a WebSocket lives
	// for hours, and the hub has its own metrics. The API names its own
	// routes, because the outer mux only sees "/" for all of them.
	routeOf := func(r *http.Request) string {
		_, pattern := routes.Handler(r)
		switch pattern {
		case "GET /stream/book":
			return "" // not timed
		case "/":
			pattern = apiServer.Route(r)
		}
		if pattern == "" {
			return telemetry.RouteUnmatched
		}
		return pattern
	}

	srv := &http.Server{
		Handler: metrics.InstrumentHTTP(routes, routeOf),
		// Timeouts protect against slow or malicious clients holding
		// connections open forever. The zero value means "no timeout",
		// which is a classic production bug.
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	// Shutdown does NOT wait for or close hijacked connections (WebSockets), so
	// stop the hub as shutdown begins: every stream gets a close frame saying
	// "server shutting down" instead of hanging until the process exits.
	srv.RegisterOnShutdown(stopHub)

	// The private admin listener: /metrics and /debug/pprof, started before
	// the public one so the first scrape already sees a running server. Its
	// Close is deferred, so it runs when run returns, after the public server
	// has shut down; it has nothing to drain, so Close (not Shutdown) is fine.
	if cfg.AdminAddr != "" {
		adminLn, err := net.Listen("tcp", cfg.AdminAddr)
		if err != nil {
			return fmt.Errorf("admin listen on %s: %w", cfg.AdminAddr, err)
		}
		admin := adminServer(adminHandler(metrics))
		admin.ErrorLog = srv.ErrorLog
		go func() { _ = admin.Serve(adminLn) }()
		defer admin.Close()
		logger.Info("admin listening", "addr", adminLn.Addr().String())
	}

	// Listen synchronously so a bad address or busy port fails startup
	// immediately with a clear error, before we report "listening".
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	logger.Info("http listening", "addr", ln.Addr().String())

	// Serve blocks, so it runs in its own goroutine. The buffered channel
	// (capacity 1) lets the goroutine deliver its error and exit even if
	// nobody is receiving any more, so it never leaks.
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	// Wait for whichever happens first: a server failure or a shutdown
	// signal.
	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	// Graceful shutdown gets a fresh, bounded context. The original ctx is
	// already canceled, so reusing it would make Shutdown give up instantly.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	// Shutdown stops accepting connections and waits for in-flight requests.
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	logger.Info("stopped cleanly")
	return nil
}
