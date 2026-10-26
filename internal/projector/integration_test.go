//go:build integration

// Integration tests against a REAL PostgreSQL, started in Docker by
// testcontainers-go (ADR-0003). They are behind the "integration" build tag,
// so plain `go test ./...` stays fast and needs no Docker:
//
//	go test -tags=integration ./internal/projector     (or: make test-integration)
//
// One container serves the whole package; every test gets its own freshly
// created database, so tests cannot see each other's rows.
package projector

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

var baseURL string

func TestMain(m *testing.M) {
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:17-alpine", // pinned, so results are reproducible
		postgres.WithDatabase("cex"), postgres.WithUsername("cex"), postgres.WithPassword("cex"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "starting PostgreSQL in Docker (is Docker running?):", err)
		os.Exit(1)
	}
	if baseURL, err = pg.ConnectionString(ctx, "sslmode=disable"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = testcontainers.TerminateContainer(pg)
	os.Exit(code)
}

var dbCount atomic.Int64

// freshDB creates a new, empty database for one test.
func freshDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := fmt.Sprintf("projection_%d", dbCount.Add(1))
	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// history writes a realistic journal the way the engine does: each command
// is applied to a State (to know which orders exist) and appended to the
// journal. Funding, crossing orders, partial fills, IOC, self-trades,
// cancels, rejections and exact retries all appear.
type history struct {
	t       *testing.T
	j       *journal.Journal
	st      *state.State
	rng     *rand.Rand
	seq     domain.Seq
	orders  int
	fills   int
	sent    []command.Command
	placeOK int
}

func newHistory(t *testing.T, path string) *history {
	t.Helper()
	return newHistorySeeded(t, path, 26)
}

// newHistorySeeded is newHistory with a chosen random seed: a different seed
// writes a different (but equally valid) history.
func newHistorySeeded(t *testing.T, path string, seed uint64) *history {
	t.Helper()
	j, _, err := journal.Open(path, journal.Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return &history{t: t, j: j, st: state.New(), rng: rand.New(rand.NewPCG(seed, 10))}
}

func (h *history) add(n int) {
	h.t.Helper()
	const operator domain.UserID = 99
	for range n {
		h.seq++
		user := domain.UserID(1 + h.rng.IntN(4))
		c := command.Command{Seq: h.seq, User: user, Key: fmt.Sprintf("k%d", h.seq)}
		switch r := h.rng.IntN(100); {
		case h.seq <= 8 || r < 8:
			c.User = operator
			c.Body = command.Fund{To: user, Asset: domain.USDT, Amount: (1_000 + h.rng.Int64N(50_000)) * domain.AtomsPerUSDT}
			if h.rng.IntN(2) == 0 {
				c.Body = command.Fund{To: user, Asset: domain.BTC, Amount: 1_000 + h.rng.Int64N(20_000)}
			}
		case r < 78:
			p := command.PlaceOrder{Side: domain.Buy, Price: domain.Price(59_990 + h.rng.Int64N(21)),
				Qty: domain.Qty(1 + h.rng.Int64N(3_000)), TIF: domain.GTC}
			if h.rng.IntN(2) == 0 {
				p.Side = domain.Sell
			}
			if h.rng.IntN(4) == 0 {
				p.TIF = domain.IOC
			}
			c.Body = p
		case r < 92:
			c.Body = command.CancelOrder{Order: domain.OrderID(1 + h.rng.IntN(h.orders+1))}
		default: // an exact retry of an earlier request
			prev := h.sent[h.rng.IntN(len(h.sent))]
			c.User, c.Key, c.Body = prev.User, prev.Key, prev.Body
		}
		fx, err := h.st.Apply(c)
		if err != nil {
			h.t.Fatal(err)
		}
		if _, ok := c.Body.(command.PlaceOrder); ok && fx.Result.Accepted() && !fx.Result.Duplicate {
			h.orders++
		}
		if !fx.Result.Duplicate {
			h.fills += len(fx.Result.Fills)
		}
		if err := h.j.Append(c); err != nil {
			h.t.Fatal(err)
		}
		h.sent = append(h.sent, c)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, query string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustNew(t *testing.T, cfg Config) *Projector {
	t.Helper()
	p, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func catchUp(t *testing.T, p *Projector) int {
	t.Helper()
	n, err := p.CatchUp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func reconcile(t *testing.T, p *Projector) {
	t.Helper()
	if err := p.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// --- tests -----------------------------------------------------------------

// TestProjectionMatchesTheEngine: project 600 commands, then prove the SQL
// matches the engine's own state at the same Seq, and that the row counts
// match what actually happened.
func TestProjectionMatchesTheEngine(t *testing.T) {
	path := filepath.Join(t.TempDir(), journal.FileName)
	h := newHistory(t, path)
	h.add(600)
	pool := freshDB(t)

	p := mustNew(t, Config{JournalPath: path, Pool: pool, BatchSize: 64})
	if n := catchUp(t, p); n != 600 || p.ProjectedSeq() != 600 {
		t.Fatalf("projected %d commands to seq %d, want 600", n, p.ProjectedSeq())
	}
	reconcile(t, p)

	if got := count(t, pool, `select count(*) from commands`); got != 600 {
		t.Errorf("commands rows = %d, want 600", got)
	}
	if got := count(t, pool, `select count(*) from trades`); got != h.fills {
		t.Errorf("trades rows = %d, want %d fills", got, h.fills)
	}
	if got := count(t, pool, `select count(*) from orders`); got != h.orders {
		t.Errorf("orders rows = %d, want %d accepted orders", got, h.orders)
	}
	open := count(t, pool, `select count(*) from orders where open_lots > 0`)
	resting := 0
	for _, l := range append(h.st.Depth(0).Bids, h.st.Depth(0).Asks...) {
		resting += l.Orders
	}
	if open != resting {
		t.Errorf("SQL has %d open orders, the book has %d", open, resting)
	}
	if got := count(t, pool, `select count(*) from commands where duplicate`); got == 0 {
		t.Error("the history contained no duplicates; the test would not cover them")
	}
}

// TestFollowsAGrowingJournal: the engine keeps appending while the projector
// runs. Each CatchUp picks up exactly the new commands.
func TestFollowsAGrowingJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), journal.FileName)
	h := newHistory(t, path)
	pool := freshDB(t)
	p := mustNew(t, Config{JournalPath: path, Pool: pool, BatchSize: 50})

	for _, n := range []int{120, 0, 1, 333} {
		h.add(n)
		if got := catchUp(t, p); got != n {
			t.Fatalf("after appending %d: projected %d", n, got)
		}
		reconcile(t, p)
	}
}

// TestCrashBeforeCommitLeavesNoTrace: the third transaction "crashes" after
// writing all its rows, just before COMMIT. Nothing of it may be visible, the
// checkpoint must still mark the end of the second batch, and a new
// projector must finish the job without a single duplicate row.
func TestCrashBeforeCommitLeavesNoTrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), journal.FileName)
	h := newHistory(t, path)
	h.add(500)
	pool := freshDB(t)

	crashing := mustNew(t, Config{JournalPath: path, Pool: pool, BatchSize: 100, Hooks: Hooks{
		BeforeCommit: func(last domain.Seq) error {
			if last == 300 {
				return fmt.Errorf("simulated crash before commit")
			}
			return nil
		},
	}})
	if _, err := crashing.CatchUp(context.Background()); err == nil {
		t.Fatal("CatchUp did not report the failed transaction")
	}
	if got := count(t, pool, `select seq from projection_checkpoint`); got != 200 {
		t.Fatalf("checkpoint = %d after the crash, want 200 (end of the last committed batch)", got)
	}
	if got := count(t, pool, `select count(*) from commands`); got != 200 {
		t.Fatalf("%d command rows visible, want 200: the rolled-back batch leaked", got)
	}
	if _, err := crashing.CatchUp(context.Background()); err == nil {
		t.Fatal("a broken projector kept going; its replay state is ahead of the database")
	}

	fresh := mustNew(t, Config{JournalPath: path, Pool: pool, BatchSize: 100})
	if n := catchUp(t, fresh); n != 300 {
		t.Fatalf("the new projector projected %d, want the remaining 300", n)
	}
	reconcile(t, fresh)
	if got := count(t, pool, `select count(*) from commands`); got != 500 {
		t.Errorf("commands rows = %d, want exactly 500", got)
	}
}

// TestRebuildIntoAnEmptyDatabaseIsIdentical: the read model is disposable.
// Projecting the same journal into a brand-new database, even with
// different transaction boundaries, must produce exactly the same tables.
func TestRebuildIntoAnEmptyDatabaseIsIdentical(t *testing.T) {
	path := filepath.Join(t.TempDir(), journal.FileName)
	h := newHistory(t, path)
	h.add(400)

	a, b := freshDB(t), freshDB(t)
	catchUp(t, mustNew(t, Config{JournalPath: path, Pool: a, BatchSize: 256}))
	catchUp(t, mustNew(t, Config{JournalPath: path, Pool: b, BatchSize: 7}))

	for table, key := range map[string]string{ // each table in its primary-key order
		"projection_checkpoint": "id",
		"commands":              "seq",
		"orders":                "order_id",
		"trades":                "seq, fill_index",
		"ledger_journals":       "seq, journal_index",
		"ledger_lines":          "seq, journal_index, line_index",
		"account_balances":      "user_id, asset, bucket",
	} {
		da, db := dump(t, a, table, key), dump(t, b, table, key)
		if len(da) == 0 || !slices.Equal(da, db) {
			t.Errorf("table %s differs between the two rebuilds (%d vs %d rows)", table, len(da), len(db))
		}
	}
}

// TestWaitsForAnIncompleteLastFrame: while the engine is in the middle of
// appending, the last frame is incomplete. The projector must take the
// complete frames, wait, and pick up the last one once it is whole.
func TestWaitsForAnIncompleteLastFrame(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.wal")
	h := newHistory(t, full)
	h.add(51)
	h50 := newHistory(t, filepath.Join(dir, "first50.wal"))
	h50.add(50) // same seed: the same first 50 frames
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "first50.wal"))
	if err != nil {
		t.Fatal(err)
	}
	cut := fi.Size() + (int64(len(data))-fi.Size())/2 // halfway through frame 51

	live := filepath.Join(dir, journal.FileName)
	if err := os.WriteFile(live, data[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, Config{JournalPath: live, Pool: freshDB(t)})
	if n := catchUp(t, p); n != 50 {
		t.Fatalf("with half of frame 51 on disk: projected %d, want 50", n)
	}
	f, err := os.OpenFile(live, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data[cut:]); err != nil { // the writer finishes the frame
		t.Fatal(err)
	}
	f.Close()
	if n := catchUp(t, p); n != 1 || p.ProjectedSeq() != 51 {
		t.Fatalf("after the frame completed: projected %d to seq %d, want 1 to 51", n, p.ProjectedSeq())
	}
	reconcile(t, p)
}

// TestCheckpointAheadOfTheJournalIsRefused: a database that has seen more
// commands than the journal holds belongs to a different history.
func TestCheckpointAheadOfTheJournalIsRefused(t *testing.T) {
	dir := t.TempDir()
	long := filepath.Join(dir, "long.wal")
	newHistory(t, long).add(100)
	short := filepath.Join(dir, "short.wal")
	newHistory(t, short).add(40)

	pool := freshDB(t)
	catchUp(t, mustNew(t, Config{JournalPath: long, Pool: pool}))
	_, err := New(context.Background(), Config{JournalPath: short, Pool: pool})
	if err == nil || !strings.Contains(err.Error(), "ahead of the journal") {
		t.Fatalf("New with a shorter journal: %v, want a refusal", err)
	}
}

// TestTwoProjectorsCannotBothWrite: the conditional checkpoint update turns a
// second writer's batch into a rollback instead of duplicated rows.
func TestTwoProjectorsCannotBothWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), journal.FileName)
	h := newHistory(t, path)
	h.add(100)
	pool := freshDB(t)

	first := mustNew(t, Config{JournalPath: path, Pool: pool})
	second := mustNew(t, Config{JournalPath: path, Pool: pool}) // both start at checkpoint 0
	catchUp(t, first)
	if _, err := second.CatchUp(context.Background()); err == nil || !strings.Contains(err.Error(), "another projector") {
		t.Fatalf("second writer: %v, want the checkpoint guard to refuse it", err)
	}
	if got := count(t, pool, `select count(*) from commands`); got != 100 {
		t.Errorf("commands rows = %d, want 100 (no duplicates)", got)
	}
}

// dump returns every row of a table as text, ordered by orderBy.
func dump(t *testing.T, pool *pgxpool.Pool, table, orderBy string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), "select * from "+table+" order by "+orderBy)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(v...))
	}
	return out
}

// TestADifferentJournalIsRefused: a database built from one journal must not
// accept another one, even a LONGER one (which the "ahead" check alone would
// let through). The command recorded at the checkpoint gives it away.
func TestADifferentJournalIsRefused(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original.wal")
	newHistorySeeded(t, original, 1).add(100)
	other := filepath.Join(dir, "other.wal")
	newHistorySeeded(t, other, 2).add(150)

	pool := freshDB(t)
	catchUp(t, mustNew(t, Config{JournalPath: original, Pool: pool}))
	_, err := New(context.Background(), Config{JournalPath: other, Pool: pool})
	if err == nil || !strings.Contains(err.Error(), "belongs to another journal") {
		t.Fatalf("New with a different, longer journal: %v, want a refusal", err)
	}
	// The SAME journal, of course, is still accepted.
	if _, err := New(context.Background(), Config{JournalPath: original, Pool: pool}); err != nil {
		t.Fatalf("the original journal was refused: %v", err)
	}
}
