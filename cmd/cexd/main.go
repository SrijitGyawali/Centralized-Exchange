// Command cexd is the exchange server.
//
// This file is the composition root: the one place that reads the
// environment, constructs every component and wires them together. It
// contains no business logic. As the project grows, run() gains steps in this
// order:
//
//  1. load config
//  2. open the journal and replay it into state (week 4)
//  3. start the engine goroutine (week 3)
//  4. start projector and market data (week 6)
//  5. serve HTTP
//  6. on SIGINT/SIGTERM shut down in reverse order: stop admitting, drain the
//     engine, flush the journal, then close everything else
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

	// Until the engine exists the server is alive but never ready, which is
	// exactly what a real deployment should report while replay is running.
	ready := func() error { return errors.New("engine not started") }

	srv := &http.Server{
		Handler: api.NewRouter(ready),
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
