package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The drill's accounts: local test credentials for a throwaway server.
const users = 16

func adminToken() string     { return "drill-admin-token-0001" }
func userToken(i int) string { return fmt.Sprintf("drill-user-token-%04d", i) }
func userID(i int) int       { return 2001 + i }
func tokensJSON() string {
	recs := []string{`{"token":"` + adminToken() + `","user_id":"1","admin":true}`}
	for i := range users {
		recs = append(recs, fmt.Sprintf(`{"token":"%s","user_id":"%d"}`, userToken(i), userID(i)))
	}
	return "[" + strings.Join(recs, ",") + "]"
}

// buildServer compiles cmd/cexd into dir, exactly as a deployment would.
func buildServer(dir string) (string, error) {
	bin := filepath.Join(dir, "cexd")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", bin, "./cmd/cexd").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("build: %w\n%s", err, out)
	}
	return bin, nil
}

// server is one cexd process on a data directory.
type server struct {
	bin, dataDir, logPath string
	base                  string
	cmd                   *exec.Cmd
	done                  chan error
	log                   *os.File
}

// recovered is what cexd logged about its startup recovery.
type recovered struct {
	Commands       int    `json:"commands"`
	LastSeq        uint64 `json:"last_seq"`
	TruncatedBytes int64  `json:"truncated_bytes"`
	Digest         string `json:"digest"`
	Took           string `json:"took"`
}

func freeAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	return ln.Addr().String(), nil
}

// start launches the server and waits until /readyz answers 200. It returns
// the wall time from launch to ready and what recovery reported.
func (s *server) start() (time.Duration, recovered, error) {
	addr, err := freeAddr()
	if err != nil {
		return 0, recovered{}, err
	}
	s.base = "http://" + addr
	if s.log, err = os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return 0, recovered{}, err
	}
	s.cmd = exec.Command(s.bin)
	for _, kv := range os.Environ() { // keep PATH etc., never a developer's CEX_ settings
		if k, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(strings.ToUpper(k), "CEX_") {
			s.cmd.Env = append(s.cmd.Env, kv)
		}
	}
	s.cmd.Env = append(s.cmd.Env,
		"CEX_HTTP_ADDR="+addr, "CEX_ADMIN_ADDR=off", "CEX_DATA_DIR="+s.dataDir,
		"CEX_API_TOKENS="+tokensJSON(), "CEX_RATE_PER_SECOND=100000", "CEX_RATE_BURST=100000",
		"CEX_REQUEST_TIMEOUT=5s", "CEX_WRITE_TIMEOUT=10s")
	s.cmd.Stdout, s.cmd.Stderr = s.log, s.log
	launched := time.Now()
	if err := s.cmd.Start(); err != nil {
		return 0, recovered{}, err
	}
	s.done = make(chan error, 1)
	go func(c *exec.Cmd) { s.done <- c.Wait() }(s.cmd)

	client := &http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); {
		select {
		case err := <-s.done:
			s.cmd = nil
			return 0, recovered{}, fmt.Errorf("server exited during startup: %v (see %s)", err, s.logPath)
		default:
		}
		if resp, err := client.Get(s.base + "/readyz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready := time.Since(launched)
				rec, err := s.lastRecovery()
				return ready, rec, err
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.kill()
	return 0, recovered{}, fmt.Errorf("server never became ready (see %s)", s.logPath)
}

// kill ends the process abruptly (SIGKILL on Unix, TerminateProcess on
// Windows): no shutdown hook runs, no buffer is flushed by cexd. Every
// stop in the drills is a kill; graceful shutdown is tested elsewhere.
func (s *server) kill() {
	if s.cmd != nil {
		_ = s.cmd.Process.Kill()
		<-s.done
		s.cmd = nil
	}
	if s.log != nil {
		_ = s.log.Close()
		s.log = nil
	}
}

// lastRecovery reads the most recent "recovered" line of the server's JSON
// log: commands replayed, last Seq, torn bytes cut off, digest, duration.
func (s *server) lastRecovery() (recovered, error) {
	f, err := os.Open(s.logPath)
	if err != nil {
		return recovered{}, err
	}
	defer f.Close()
	var last recovered
	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			Msg string `json:"msg"`
			recovered
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Msg == "recovered" {
			last, found = line.recovered, true
		}
	}
	if !found {
		return recovered{}, fmt.Errorf("no recovery line in %s", s.logPath)
	}
	return last, sc.Err()
}
