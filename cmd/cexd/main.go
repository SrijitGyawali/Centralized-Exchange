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
//  4. serve HTTP; /readyz answers 200 once recovery has succeeded
//  5. on SIGINT/SIGTERM shut down in reverse order: stop HTTP, then stop
//     admitting, drain the engine and close the journal
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
	"syscall"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/api"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/config"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/node"
)

// version is overwritten at build time:
//
//	go build -ldflags "-X main.version=v0.1.0" ./cmd/cexd
var version = "dev"

// main stays tiny. All the work happens in run, which returns an error
// instead of calling os.Exit or log.Fatal. Deferred cleanup therefore always
// runs, and tests can call run directly.
func main() {
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

	// Recovery: replay the journal into a fresh state and start the engine.
	// If the journal is corrupt or the recovered state breaks an invariant,
	// the server refuses to start rather than trade on a wrong state.
	nd, err := node.Open(node.Config{Dir: cfg.DataDir, Engine: engine.DefaultConfig()})
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
		logger.Info("engine drained and journal closed")
	}()
	logger.Info("recovered",
		"commands", nd.Recovery.Commands,
		"last_seq", nd.Recovery.LastSeq,
		"truncated_bytes", nd.Recovery.TruncatedBytes,
		"digest", fmt.Sprintf("%x", nd.Recovery.Digest),
		"took", nd.Recovery.Took.String(),
	)

	srv := &http.Server{
		Handler: api.NewRouter(nd.Ready), // ready unless the engine has halted
		// Timeouts protect against slow or malicious clients holding
		// connections open forever. The zero value means "no timeout",
		// which is a classic production bug.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
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
