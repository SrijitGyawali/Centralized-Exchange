package projector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// Config tells a projector where to read and where to write.
type Config struct {
	JournalPath string        // the engine's journal, read-only
	Pool        *pgxpool.Pool // the read-model database
	BatchSize   int           // commands per SQL transaction (default 256)
	Hooks       Hooks         // test-only failpoints
}

// Hooks are failpoints for tests; production leaves them nil.
type Hooks struct {
	// BeforeCommit runs inside the transaction after every row of a batch is
	// written, just before COMMIT. Returning an error rolls the batch back:
	// the worst possible moment for a crash.
	BeforeCommit func(lastSeq domain.Seq) error
}

// Projector keeps the SQL read model in step with the journal.
//
// It never asks the engine for anything. It reads the same durable journal,
// replays every command through its OWN private state.State, and records
// the resulting effects. Because Apply is deterministic, its state, and
// therefore every row it writes, matches what the engine did exactly. It
// does not depend on any live, in-memory event stream that could drop
// messages.
//
// Not safe for concurrent use: one goroutine drives it.
type Projector struct {
	cfg       Config
	st        *state.State  // replay state, always at exactly the checkpoint
	offset    int64         // byte offset in the journal of the next unread frame
	next      domain.Seq    // Seq of the next frame expected
	projected atomic.Uint64 // the committed checkpoint, readable from other goroutines
	broken    error         // set when the replay state got ahead of the database
}

// item is one command and what it did, waiting to be written.
type item struct {
	cmd command.Command
	fx  state.Effects
}

var (
	errCaughtUp     = errors.New("caught up to the checkpoint")
	errBatchFull    = errors.New("batch full")
	errOtherJournal = errors.New("journal does not match the database")
)

// New migrates the schema, reads the checkpoint, and rebuilds the private
// replay state by applying journal commands 1..checkpoint WITHOUT writing
// anything. The next CatchUp continues exactly where the last committed
// transaction ended, so nothing is skipped and nothing is written twice.
func New(ctx context.Context, cfg Config) (*Projector, error) {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 256
	}
	if err := Migrate(ctx, cfg.Pool); err != nil {
		return nil, err
	}
	var (
		cp       int64
		lastUser int64
		lastKey  string
	)
	if err := cfg.Pool.QueryRow(ctx, `select seq, last_user_id, last_key from projection_checkpoint`).
		Scan(&cp, &lastUser, &lastKey); err != nil {
		return nil, fmt.Errorf("projector: reading checkpoint: %w", err)
	}
	checkpoint := domain.Seq(cp)

	p := &Projector{cfg: cfg, st: state.New(), next: checkpoint + 1}
	f, err := os.Open(cfg.JournalPath)
	switch {
	case errors.Is(err, os.ErrNotExist) && checkpoint == 0:
		return p, nil // nothing journaled yet: start from the beginning
	case err != nil:
		return nil, fmt.Errorf("projector: %w", err)
	}
	defer f.Close()

	res, err := journal.Scan(f, func(c command.Command) error {
		if c.Seq > checkpoint {
			return errCaughtUp
		}
		if c.Seq == checkpoint && (int64(c.User) != lastUser || c.Key != lastKey) {
			// Same Seq, different command: this journal is not the one the
			// database was built from. Writing on would mix two histories.
			return errOtherJournal
		}
		_, err := p.st.Apply(c)
		return err
	})
	if errors.Is(err, errOtherJournal) {
		return nil, fmt.Errorf("projector: the journal's command at seq %d differs from the one the database "+
			"recorded; this database belongs to another journal: drop it to rebuild", checkpoint)
	}
	if err != nil && !errors.Is(err, errCaughtUp) {
		return nil, fmt.Errorf("projector: rebuilding to checkpoint %d: %w", checkpoint, err)
	}
	if res.LastSeq < checkpoint {
		// The database has seen commands this journal does not contain: the
		// journal was replaced or lost. Writing on would mix two histories.
		return nil, fmt.Errorf("projector: database checkpoint %d is ahead of the journal (last seq %d); "+
			"drop the read-model database to rebuild it", checkpoint, res.LastSeq)
	}
	p.offset = res.ValidBytes
	p.projected.Store(uint64(checkpoint))
	return p, nil
}

// ProjectedSeq is the last Seq committed to the database. Safe to call from
// any goroutine.
func (p *Projector) ProjectedSeq() domain.Seq { return domain.Seq(p.projected.Load()) }

// CatchUp projects every complete frame written since the last call, in
// transactions of at most BatchSize commands, and returns how many commands
// it committed. An incomplete last frame (the engine is mid-append) is left
// for the next call.
//
// If a transaction fails, the private replay state is already AHEAD of the
// database, so this projector is unusable from then on: CatchUp keeps
// returning the error, and the caller builds a new one with New, which
// rebuilds the state from the committed checkpoint. Run does that.
func (p *Projector) CatchUp(ctx context.Context) (int, error) {
	if p.broken != nil {
		return 0, p.broken
	}
	f, err := os.Open(p.cfg.JournalPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("projector: %w", err)
	}
	defer f.Close()

	total := 0
	for {
		// ScanFrom reads through a buffer, so seek to the exact frame
		// boundary before every pass.
		if _, err := f.Seek(p.offset, io.SeekStart); err != nil {
			return total, fmt.Errorf("projector: %w", err)
		}
		var items []item
		res, err := journal.ScanFrom(f, p.next, func(c command.Command) error {
			if len(items) == p.cfg.BatchSize {
				return errBatchFull
			}
			fx, err := p.st.Apply(c)
			if err != nil {
				return err
			}
			items = append(items, item{c, fx})
			return nil
		})
		full := errors.Is(err, errBatchFull)
		if err != nil && !full {
			p.broken = fmt.Errorf("projector: reading the journal: %w", err)
			return total, p.broken
		}
		if len(items) == 0 {
			return total, nil
		}
		if err := p.write(ctx, items); err != nil {
			p.broken = fmt.Errorf("projector: writing seq %d..%d: %w", items[0].cmd.Seq, items[len(items)-1].cmd.Seq, err)
			return total, p.broken
		}
		p.offset += res.ValidBytes
		p.next = items[len(items)-1].cmd.Seq + 1
		total += len(items)
		if !full {
			return total, nil
		}
	}
}

// write records a batch and moves the checkpoint in ONE transaction. Either
// every row of the batch and the new checkpoint become visible together, or
// none of it does. That is what makes a crash at any moment harmless: on
// restart, New resumes from a checkpoint that exactly matches the rows.
//
// The checkpoint row is LOCKED and checked first (SELECT ... FOR UPDATE).
// If it is not where this projector expects, something else, such as a
// second projector on the same database, has moved it. The batch is refused
// before a single row is written. Two projectors racing therefore queue on
// the lock instead of both writing.
func (p *Projector) write(ctx context.Context, items []item) error {
	tx, err := p.cfg.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // a no-op after Commit

	prev := int64(items[0].cmd.Seq) - 1
	var current int64
	if err := tx.QueryRow(ctx, `select seq from projection_checkpoint for update`).Scan(&current); err != nil {
		return err
	}
	if current != prev {
		return fmt.Errorf("checkpoint is %d, expected %d: another projector is writing to this database", current, prev)
	}

	// pgx.Batch sends all statements in one network round trip.
	b := &pgx.Batch{}
	for _, it := range items {
		stmts, err := plan(it.cmd, it.fx)
		if err != nil {
			return err
		}
		for _, s := range stmts {
			b.Queue(s.sql, s.args...)
		}
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil { // Close reports the first failed statement
		return err
	}

	last := items[len(items)-1].cmd.Seq
	lastCmd := items[len(items)-1].cmd
	if _, err := tx.Exec(ctx, `update projection_checkpoint set seq = $1, last_user_id = $2, last_key = $3`,
		int64(last), int64(lastCmd.User), lastCmd.Key); err != nil {
		return err
	}
	if h := p.cfg.Hooks.BeforeCommit; h != nil {
		if err := h(last); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	p.projected.Store(uint64(last))
	return nil
}

// Reconcile checks the database against the projector's own replay state AT
// THE SAME SEQ (the checkpoint):
//   - the checkpoint equals the replay state's last Seq;
//   - every account balance in SQL equals the state's balance, and vice
//     versa (including the system issuance accounts);
//   - every asset's ledger lines sum to zero.
//
// Comparing a lagging database with the live engine would raise false
// alarms; comparing at the same Seq makes any difference a real one.
func (p *Projector) Reconcile(ctx context.Context) error {
	var cp int64
	if err := p.cfg.Pool.QueryRow(ctx, `select seq from projection_checkpoint`).Scan(&cp); err != nil {
		return err
	}
	if domain.Seq(cp) != p.st.LastSeq() {
		return fmt.Errorf("checkpoint %d differs from the replay state's seq %d", cp, p.st.LastSeq())
	}

	type key struct {
		user          int64
		asset, bucket string
	}
	want := map[key]int64{}
	for _, b := range p.st.Balances() {
		if b.Available != 0 {
			want[key{int64(b.User), b.Asset.String(), "available"}] = b.Available
		}
		if b.Reserved != 0 {
			want[key{int64(b.User), b.Asset.String(), "reserved"}] = b.Reserved
		}
	}
	for _, a := range []domain.Asset{domain.BTC, domain.USDT} {
		if issued := p.st.Issued(a); issued != 0 {
			want[key{0, a.String(), "issuance"}] = -issued
		}
	}

	rows, err := p.cfg.Pool.Query(ctx, `select user_id, asset, bucket, amount from account_balances where amount <> 0`)
	if err != nil {
		return err
	}
	got := map[key]int64{}
	for rows.Next() {
		var k key
		var amount int64
		if err := rows.Scan(&k.user, &k.asset, &k.bucket, &amount); err != nil {
			return err
		}
		got[k] = amount
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for k, v := range want {
		if got[k] != v {
			return fmt.Errorf("balance drift at seq %d: %+v is %d in SQL, %d in the state", cp, k, got[k], v)
		}
	}
	for k, v := range got {
		if want[k] != v {
			return fmt.Errorf("balance drift at seq %d: %+v is %d in SQL, %d in the state", cp, k, v, want[k])
		}
	}

	var unbalanced int
	if err := p.cfg.Pool.QueryRow(ctx,
		`select count(*) from (select asset from ledger_lines group by asset having sum(amount) <> 0) t`,
	).Scan(&unbalanced); err != nil {
		return err
	}
	if unbalanced != 0 {
		return fmt.Errorf("the SQL ledger does not balance for %d asset(s)", unbalanced)
	}
	return nil
}
