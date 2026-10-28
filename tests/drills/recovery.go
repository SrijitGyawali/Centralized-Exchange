package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/node"
)

// recoveryRun is how long startup recovery takes for a journal of a given
// size: the plan's budget asks to measure at 100k, then 1M commands, before
// choosing a target.
type recoveryRun struct {
	Commands        int      `json:"commands"`
	JournalBytes    int64    `json:"journal_bytes"`
	GenerateSeconds float64  `json:"generate_seconds"`
	Recoveries      []string `json:"recoveries"` // node.Open durations, 3 runs
	RecoveryMedian  string   `json:"recovery_median"`
	CommandsPerSec  float64  `json:"replay_commands_per_second"`
	HeapMiB         float64  `json:"heap_in_use_after_recovery_mib"`
}

// recoveryDrill writes n commands through a real node (journal, group
// commit, the same engine as cexd), then times node.Open, which is exactly
// the recovery cexd runs at startup: scan every frame, verify both CRCs,
// Apply every command, check every invariant, compute the digest.
func recoveryDrill(dir string, n int, seed uint64) (recoveryRun, error) {
	r := recoveryRun{Commands: n}
	nd, err := node.Open(node.Config{Dir: dir, Engine: engine.Config{QueueSize: 4096, BatchSize: 64}})
	if err != nil {
		return r, err
	}
	ctx := context.Background()
	start := time.Now()
	for i := range users { // generous funding, as in the load tests
		for _, f := range []command.Fund{
			{To: domain.UserID(userID(i)), Asset: domain.USDT, Amount: 10_000_000 * domain.AtomsPerUSDT},
			{To: domain.UserID(userID(i)), Asset: domain.BTC, Amount: 10_000 * domain.LotsPerBTC},
		} {
			if _, err := nd.Engine.Submit(ctx, 1, fmt.Sprintf("fund-%d-%v", i, f.Asset), f); err != nil {
				nd.Close()
				return r, err
			}
		}
	}
	var issued atomic.Int64
	issued.Store(int64(2 * users))
	var wg sync.WaitGroup
	// The first error any generator hit. A mutex, not atomic.Value: Value
	// panics when two goroutines store errors of different concrete types.
	var (
		errMu    sync.Mutex
		firstErr error
	)
	for g := range 64 {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(seed, uint64(g)))
			user := domain.UserID(userID(g % users))
			var resting []domain.OrderID
			for i := 0; issued.Add(1) <= int64(n); i++ {
				var body command.Body
				switch p := rng.IntN(100); {
				case p < 20 && len(resting) > 0:
					body, resting = command.CancelOrder{Order: resting[0]}, resting[1:]
				case p < 60:
					side, price := domain.Buy, domain.Price(60_000-1-rng.IntN(50))
					if rng.IntN(2) == 0 {
						side, price = domain.Sell, domain.Price(60_000+1+rng.IntN(50))
					}
					body = command.PlaceOrder{Side: side, Price: price, Qty: domain.Qty(1 + rng.IntN(50)), TIF: domain.GTC}
				default:
					side, price := domain.Buy, domain.Price(60_051)
					if rng.IntN(2) == 0 {
						side, price = domain.Sell, domain.Price(59_949)
					}
					body = command.PlaceOrder{Side: side, Price: price, Qty: domain.Qty(1 + rng.IntN(100)), TIF: domain.IOC}
				}
				res, err := nd.Engine.Submit(ctx, user, fmt.Sprintf("g%02d-%07d", g, i), body)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					return
				}
				if res.Accepted() && res.Outcome == book.Rested {
					resting = append(resting, res.Order)
				}
			}
		})
	}
	wg.Wait()
	r.GenerateSeconds = time.Since(start).Seconds()
	if err := nd.Close(); err != nil {
		return r, err
	}
	if firstErr != nil {
		return r, firstErr
	}
	fi, err := os.Stat(filepath.Join(dir, journal.FileName))
	if err != nil {
		return r, err
	}
	r.JournalBytes = fi.Size()

	var took []time.Duration
	for range 3 {
		runtime.GC()
		nd, err := node.Open(node.Config{Dir: dir})
		if err != nil {
			return r, err
		}
		took = append(took, nd.Recovery.Took)
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		r.HeapMiB = float64(ms.HeapInuse) / (1 << 20)
		if err := nd.Close(); err != nil {
			return r, err
		}
	}
	for _, t := range took {
		r.Recoveries = append(r.Recoveries, t.Round(time.Millisecond).String())
	}
	slices.Sort(took)
	med := took[len(took)/2]
	r.RecoveryMedian = med.Round(time.Millisecond).String()
	r.CommandsPerSec = float64(n) / med.Seconds()
	return r, nil
}
