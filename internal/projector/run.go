package projector

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Status is what the rest of the server may know about the projection: the
// last committed Seq and whether the projector is currently healthy. Safe
// for concurrent use; Run updates it.
type Status struct {
	seq     atomic.Uint64
	healthy atomic.Bool
}

// ProjectedSeq returns the last Seq committed to the read model.
func (s *Status) ProjectedSeq() domain.Seq { return domain.Seq(s.seq.Load()) }

// Healthy reports whether the projector is running normally.
func (s *Status) Healthy() bool { return s.healthy.Load() }

// Run keeps the read model up to date until ctx ends, then returns.
//
// It never gives up and never guesses. Any failure (the database is down, a
// write is rejected, a connection drops mid-transaction) means: log it, wait,
// build a fresh Projector from the committed checkpoint, and carry on. The
// projection may LAG during an outage, but it can never skip or double a
// command, because every batch and its checkpoint commit together.
//
// Trading does not depend on any of this. The engine never waits for the
// projector, so a database outage only makes history queries stale; it
// never stops matching (ADR-0009).
func Run(ctx context.Context, cfg Config, poll time.Duration, status *Status, log *slog.Logger) {
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	backoff := time.Second
	for ctx.Err() == nil {
		p, err := New(ctx, cfg)
		if err != nil {
			status.healthy.Store(false)
			log.Warn("projector: start failed; retrying", "err", err, "in", backoff.String())
			sleep(ctx, backoff)
			backoff = min(2*backoff, 30*time.Second)
			continue
		}
		status.seq.Store(uint64(p.ProjectedSeq()))
		status.healthy.Store(true)
		log.Info("projector: following the journal", "from_seq", p.ProjectedSeq())
		backoff = time.Second

		for ctx.Err() == nil {
			n, err := p.CatchUp(ctx)
			status.seq.Store(uint64(p.ProjectedSeq()))
			if err != nil {
				if ctx.Err() == nil {
					status.healthy.Store(false)
					log.Warn("projector: catch-up failed; rebuilding from the checkpoint", "err", err)
					sleep(ctx, backoff)
				}
				break
			}
			if n == 0 {
				sleep(ctx, poll) // nothing new: look again shortly
			}
		}
	}
}

// sleep waits for d or until ctx ends, whichever comes first.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
