package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/config"
)

// healthcheck implements `cexd healthcheck`: ask the server running in this
// same container whether it is ready, and exit 0 (yes) or 1 (no).
//
// Why a subcommand: the container image is distroless, with no shell, curl
// or wget, so Docker's HEALTHCHECK cannot run `curl /readyz`. The binary
// that is already in the image can make the request itself.
//
// It asks /readyz, not /healthz: "healthy" in Compose should mean recovered
// from the journal and trading (not halted), which is what readiness means.
func healthcheck(getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("healthcheck: CEX_HTTP_ADDR %q: %w", cfg.HTTPAddr, err)
	}
	// ":8080" and "0.0.0.0:8080" listen on every interface; connect via
	// loopback, which is always one of them.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/readyz")
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: /readyz answered %d", resp.StatusCode)
	}
	return nil
}
