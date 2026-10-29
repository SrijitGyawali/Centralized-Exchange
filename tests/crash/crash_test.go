// Package crash_test is a subprocess crash harness
// that confirms acknowledged effects survive, unknown outcomes resolve by
// key, and the restored state matches the recovered log.
//
// HOW IT WORKS: the test binary re-executes ITSELF as a child process
// (os.Args[0] -test.run=^TestCrashChild$), a standard Go pattern for
// testing process exits. The child opens a node in a temp directory, sends a
// fixed 10-step trading script, and prints "ACK <step> <result JSON>" for
// every reply it receives. At one injected point it calls os.Exit
// immediately: no deferred functions, no cleanup, no closing of files. To the
// journal that is the same as the process being killed. The parent then
// recovers from the same directory and checks everything.
//
// FAILURE MODEL: this is a PROCESS crash. Bytes the child handed to the
// operating system before dying survive in the OS cache, so "after-write"
// (written, not yet fsynced) is expected to survive. Surviving power loss in
// that window is NOT claimed or tested here (ADR-0006).
package crash_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/node"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

const (
	envChild = "CEX_CRASH_CHILD" // "1" in the child process
	envDir   = "CEX_CRASH_DIR"   // the data directory
	envPoint = "CEX_CRASH_POINT" // where to die (see crashPoints)
	envStep  = "CEX_CRASH_STEP"  // at which script step of THIS run (1-based; 0 = never)

	crashExitCode = 77 // tells the parent "died on purpose", not "test failed"

	operator domain.UserID = 99
	alice    domain.UserID = 1
	bob      domain.UserID = 2
)

type step struct {
	user domain.UserID
	key  string
	body command.Body
}

// script is the trading session every child sends, from step 1, on every
// run. Steps already done in an earlier run come back as duplicates.
var script = []step{
	{operator, "fund-alice", command.Fund{To: alice, Asset: domain.USDT, Amount: 100_000 * domain.AtomsPerUSDT}},
	{operator, "fund-bob", command.Fund{To: bob, Asset: domain.BTC, Amount: 10 * domain.LotsPerBTC}},
	{bob, "bob-ask-1", command.PlaceOrder{Side: domain.Sell, Price: 60_100, Qty: 5_000, TIF: domain.GTC}},    // order 1 rests
	{alice, "alice-bid-1", command.PlaceOrder{Side: domain.Buy, Price: 60_200, Qty: 2_000, TIF: domain.GTC}}, // fills at 60,100: price improvement
	{alice, "alice-bid-2", command.PlaceOrder{Side: domain.Buy, Price: 59_000, Qty: 3_000, TIF: domain.GTC}}, // order 3 rests
	{bob, "bob-ask-2", command.PlaceOrder{Side: domain.Sell, Price: 58_000, Qty: 1_000, TIF: domain.IOC}},    // partially fills order 3
	{alice, "alice-cancel", command.CancelOrder{Order: 3}},                                                   // releases the rest
	{bob, "bob-ask-3", command.PlaceOrder{Side: domain.Sell, Price: 61_000, Qty: 10_000, TIF: domain.GTC}},   // order 5 rests
	{alice, "alice-bid-3", command.PlaceOrder{Side: domain.Buy, Price: 61_000, Qty: 5_000, TIF: domain.IOC}}, // sweeps two levels
	{alice, "alice-bid-1", command.PlaceOrder{Side: domain.Buy, Price: 60_200, Qty: 2_000, TIF: domain.GTC}}, // a client retry of step 4
}

// crashPoints are the five boundaries inside one command's life, in order.
// "durable" says whether the in-flight command must survive a crash there.
var crashPoints = []struct {
	name    string
	durable bool
	torn    bool // recovery must cut off a half-written frame
}{
	{"before-write", false, false}, // nothing written yet
	{"mid-write", false, true},     // half a frame on disk
	{"after-write", true, false},   // whole frame in the OS cache, not fsynced
	{"after-sync", true, false},    // fsynced, not yet applied
	{"after-apply", true, false},   // applied, reply not yet sent
}

// --- the child process -----------------------------------------------------

// TestCrashChild is not a real test. It runs only when re-executed by the
// tests below, and it is the "exchange process" that gets killed.
func TestCrashChild(t *testing.T) {
	if os.Getenv(envChild) != "1" {
		t.Skip("helper process for the crash tests; runs only when re-executed")
	}
	point := os.Getenv(envPoint)
	stepN, _ := strconv.Atoi(os.Getenv(envStep))

	// Hooks run on the engine goroutine; crashSeq is written here. An atomic
	// keeps that obviously race-free.
	var crashSeq atomic.Uint64
	die := func(name string) func(domain.Seq) {
		if name != point {
			return nil
		}
		return func(seq domain.Seq) {
			if uint64(seq) == crashSeq.Load() {
				os.Exit(crashExitCode) // dies NOW: no defers, no flush, no close
			}
		}
	}
	n, err := node.Open(node.Config{
		Dir: os.Getenv(envDir),
		JournalHooks: journal.Hooks{
			BeforeWrite: die("before-write"), MidWrite: die("mid-write"),
			AfterWrite: die("after-write"), AfterSync: die("after-sync"),
		},
		Engine: engine.Config{Hooks: engine.Hooks{AfterApply: die("after-apply")}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: open:", err)
		os.Exit(2)
	}
	if stepN > 0 { // this run's step N will get Seq = whatever was recovered + N
		crashSeq.Store(uint64(n.Recovery.LastSeq) + uint64(stepN))
	}

	for i, s := range script {
		res, err := n.Engine.Submit(context.Background(), s.user, s.key, s.body)
		if err != nil {
			fmt.Fprintln(os.Stderr, "child: submit:", err)
			os.Exit(2)
		}
		b, _ := json.Marshal(res)
		fmt.Printf("ACK %d %s\n", i+1, b) // os.Stdout is unbuffered: this line is out before any crash
	}
	if err := n.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "child: close:", err)
		os.Exit(2)
	}
}

// --- the parent: run, crash, recover, verify ------------------------------

// runChild runs one child process and returns the results it acknowledged,
// in step order, and whether it died at the injected crash point.
func runChild(t *testing.T, dir, point string, step int) (acks []command.Result, crashed bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), envChild+"=1", envDir+"="+dir, envPoint+"="+point, envStep+"="+strconv.Itoa(step))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit) && exit.ExitCode() == crashExitCode:
		crashed = true
	default:
		t.Fatalf("child failed: %v\nstderr:\n%s\nstdout:\n%s", err, stderr.String(), out)
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.SplitN(sc.Text(), " ", 3)
		if len(fields) != 3 || fields[0] != "ACK" {
			continue // the test binary's own output ("PASS", ...)
		}
		var r command.Result
		if err := json.Unmarshal([]byte(fields[2]), &r); err != nil {
			t.Fatalf("bad ACK line %q: %v", sc.Text(), err)
		}
		if fields[1] != strconv.Itoa(len(acks)+1) {
			t.Fatalf("ACK for step %s arrived out of order", fields[1])
		}
		acks = append(acks, r)
	}
	return acks, crashed
}

func openNode(t *testing.T, dir string) *node.Node {
	t.Helper()
	n, err := node.Open(node.Config{Dir: dir})
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() }) // a second Close is harmless
	return n
}

func send(t *testing.T, n *node.Node, i int) command.Result {
	t.Helper()
	s := script[i]
	r, err := n.Engine.Submit(t.Context(), s.user, s.key, s.body)
	if err != nil {
		t.Fatalf("step %d after recovery: %v", i+1, err)
	}
	return r
}

// expected runs the script once on a plain in-memory State: no engine, no
// journal, no crash. Whatever crashes happen, every step's outcome must
// match this.
func expected(t *testing.T) []command.Result {
	t.Helper()
	st := state.New()
	out := make([]command.Result, len(script))
	for i, s := range script {
		fx, err := st.Apply(command.Command{Seq: domain.Seq(i + 1), User: s.user, Key: s.key, Body: s.body})
		if err != nil {
			t.Fatal(err)
		}
		out[i] = fx.Result
	}
	return out
}

// same compares outcomes, ignoring the two fields that legitimately depend
// on WHEN a command ran: its Seq (a lost command re-runs later) and whether
// this answer was a duplicate.
func same(a, b command.Result) bool {
	a.Seq, b.Seq = 0, 0
	a.Duplicate, b.Duplicate = false, false
	return reflect.DeepEqual(a, b)
}

// verifyAgainstReplay checks the final, recovered state: invariants hold,
// and its digest equals (1) an independent replay of the journal file into
// a brand-new State, and (2) what a fresh restart recovers.
func verifyAgainstReplay(t *testing.T, n *node.Node, dir string) {
	t.Helper()
	type report struct {
		digest [32]byte
		inv    error
	}
	live, err := engine.Query(t.Context(), n.Engine, func(s engine.StateReader) report {
		return report{s.Digest(), s.CheckInvariants()}
	})
	if err != nil || live.inv != nil {
		t.Fatalf("final state: query %v, invariants %v", err, live.inv)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(filepath.Join(dir, journal.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st := state.New()
	res, err := journal.Scan(f, func(c command.Command) error { _, err := st.Apply(c); return err })
	if err != nil || res.TornBytes != 0 {
		t.Fatalf("independent replay: %+v, %v", res, err)
	}
	if st.Digest() != live.digest {
		t.Fatal("the live state differs from an independent replay of its own journal")
	}
	if again := openNode(t, dir); again.Recovery.Digest != live.digest {
		t.Fatal("a fresh restart recovered a different state")
	}
}

// TestCrashRecovery kills the exchange at each of the five crash points, at
// an early, middle and late step, then recovers and proves:
//  1. every ACKNOWLEDGED command survived, exactly once (its retry is a
//     duplicate with the same result);
//  2. the IN-FLIGHT command resolves by its key: present if it was durable
//     when the process died, otherwise it runs now;
//  3. every outcome equals the crash-free run;
//  4. the final state equals an independent replay of the log.
func TestCrashRecovery(t *testing.T) {
	want := expected(t)
	for _, p := range crashPoints {
		for _, at := range []int{1, 4, 9} {
			t.Run(fmt.Sprintf("%s/step-%d", p.name, at), func(t *testing.T) {
				t.Parallel() // each child process has its own directory
				dir := t.TempDir()

				acks, crashed := runChild(t, dir, p.name, at)
				if !crashed || len(acks) != at-1 {
					t.Fatalf("crashed=%v after %d ACKs; want a crash with %d ACKs", crashed, len(acks), at-1)
				}

				n := openNode(t, dir)
				wantRecovered := at - 1
				if p.durable {
					wantRecovered = at
				}
				if n.Recovery.Commands != wantRecovered || (n.Recovery.TruncatedBytes > 0) != p.torn {
					t.Fatalf("recovery %+v; want %d commands, torn tail %v", n.Recovery, wantRecovered, p.torn)
				}

				for i, ack := range acks { // 1. acknowledged = survived exactly once
					if !same(ack, want[i]) {
						t.Fatalf("step %d was acknowledged with %+v, crash-free run gives %+v", i+1, ack, want[i])
					}
					if r := send(t, n, i); !r.Duplicate || r.Seq != ack.Seq || !same(r, ack) {
						t.Fatalf("step %d was ACKNOWLEDGED before the crash, but its retry gave %+v", i+1, r)
					}
				}
				inflight := send(t, n, at-1) // 2. the unknown outcome, resolved by key
				if inflight.Duplicate != p.durable || !same(inflight, want[at-1]) {
					t.Fatalf("in-flight step %d: %+v; want duplicate=%v with outcome %+v", at, inflight, p.durable, want[at-1])
				}
				for i := at; i < len(script); i++ { // 3. life goes on, identically
					if r := send(t, n, i); !same(r, want[i]) {
						t.Fatalf("step %d after recovery: %+v, want %+v", i+1, r, want[i])
					}
				}
				verifyAgainstReplay(t, n, dir) // 4.
			})
		}
	}
}

// TestCleanShutdownAndRestart: no crash at all. The child sends the whole
// script and closes cleanly; after a restart every step is a duplicate.
func TestCleanShutdownAndRestart(t *testing.T) {
	want := expected(t)
	dir := t.TempDir()
	acks, crashed := runChild(t, dir, "none", 0)
	if crashed || len(acks) != len(script) {
		t.Fatalf("crashed=%v with %d ACKs; want a clean run of %d", crashed, len(acks), len(script))
	}
	n := openNode(t, dir)
	for i := range script {
		if !same(acks[i], want[i]) {
			t.Fatalf("step %d: %+v, want %+v", i+1, acks[i], want[i])
		}
		if r := send(t, n, i); !r.Duplicate || !same(r, want[i]) {
			t.Fatalf("step %d after restart: %+v, want a duplicate", i+1, r)
		}
	}
	verifyAgainstReplay(t, n, dir)
}

// TestRepeatedCrashes: crash, recover, crash again somewhere else, and again,
// then finish cleanly. Each run restarts the script from step 1, so earlier
// steps come back as duplicates and lost ones run again. Across ALL runs,
// every acknowledged outcome must equal the crash-free run.
func TestRepeatedCrashes(t *testing.T) {
	want := expected(t)
	dir := t.TempDir()
	runs := []struct {
		point string
		step  int
	}{
		{"mid-write", 3},   // torn tail at step 3
		{"after-sync", 5},  // durable but unanswered at step 5
		{"after-apply", 7}, // applied but unanswered at step 7
		{"before-write", 9},
		{"none", 0}, // finally, a clean run
	}
	for _, r := range runs {
		acks, crashed := runChild(t, dir, r.point, r.step)
		if crashed != (r.step > 0) {
			t.Fatalf("run %s/%d: crashed=%v", r.point, r.step, crashed)
		}
		for i, ack := range acks {
			if !same(ack, want[i]) {
				t.Fatalf("run %s/%d, step %d: %+v, want %+v", r.point, r.step, i+1, ack, want[i])
			}
		}
	}
	n := openNode(t, dir)
	for i := range script {
		if r := send(t, n, i); !r.Duplicate || !same(r, want[i]) {
			t.Fatalf("step %d after all crashes: %+v", i+1, r)
		}
	}
	verifyAgainstReplay(t, n, dir)
}
