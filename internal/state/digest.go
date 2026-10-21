package state

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Digest returns a SHA-256 fingerprint of the ENTIRE state: every balance,
// every open order and its reservation, every book queue in FIFO order,
// every remembered request and result, and the counters.
//
// Two states have the same digest exactly when they are the same state. That
// turns "did recovery rebuild the state correctly?" into comparing 32 bytes:
// the digest after a crash and restart must equal the digest of an
// independent replay of the same log.
//
// It writes a canonical text form into the hash. Every map is visited in
// SORTED key order, because Go randomizes map iteration and the same state
// must always hash the same. The text form is for comparing states produced
// by the same software version; it is not a stable file format.
//
// Cost is O(everything): for tests, recovery checks and the replay tool,
// never per command.
func (s *State) Digest() [32]byte {
	h := sha256.New()
	w := func(format string, args ...any) { fmt.Fprintf(h, format, args...) }

	w("seq %d order %d\n", s.lastSeq, s.lastOrder)

	for _, k := range slices.SortedFunc(maps.Keys(s.balances), compareBalanceKeys) {
		w("balance %d %d %d %d\n", k.user, k.asset, k.bucket, s.balances[k])
	}
	for _, id := range slices.Sorted(maps.Keys(s.open)) {
		o := s.open[id]
		w("open %d %d %d %d %d\n", id, o.user, o.side, o.price, o.reserved)
	}
	// Both sides in a fixed order (a slice, not a map), best price first,
	// each queue front to back: this captures time priority, not just
	// quantities.
	d := s.book.Depth(0)
	for _, side := range []domain.Side{domain.Buy, domain.Sell} {
		rows := d.Bids
		if side == domain.Sell {
			rows = d.Asks
		}
		for _, row := range rows {
			for _, o := range s.book.QueueAt(side, row.Price) {
				w("book %d %d %d %d %d %d\n", o.ID, o.User, o.Side, o.Price, o.Qty, o.Seq)
			}
		}
	}
	for _, k := range slices.SortedFunc(maps.Keys(s.dedupe), compareDedupeKeys) {
		e := s.dedupe[k]
		w("dedupe %d %q %#v %#v\n", k.user, k.key, e.body, e.result)
	}

	var sum [32]byte
	h.Sum(sum[:0])
	return sum
}

// BalanceRow is one user's holding of one asset.
type BalanceRow struct {
	User                domain.UserID
	Asset               domain.Asset
	Available, Reserved int64
}

// Balances lists every user's non-zero holdings, sorted by user then asset.
// The system issuance account is not included (see Issued).
func (s *State) Balances() []BalanceRow {
	rows := map[[2]uint64]*BalanceRow{}
	for k, v := range s.balances {
		if k.bucket == Issuance {
			continue
		}
		id := [2]uint64{uint64(k.user), uint64(k.asset)}
		r := rows[id]
		if r == nil {
			r = &BalanceRow{User: k.user, Asset: k.asset}
			rows[id] = r
		}
		if k.bucket == Available {
			r.Available = v
		} else {
			r.Reserved = v
		}
	}
	out := make([]BalanceRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b BalanceRow) int {
		return cmp.Or(cmp.Compare(a.User, b.User), cmp.Compare(a.Asset, b.Asset))
	})
	return out
}

func compareBalanceKeys(a, b balanceKey) int {
	return cmp.Or(cmp.Compare(a.user, b.user), cmp.Compare(a.asset, b.asset), cmp.Compare(a.bucket, b.bucket))
}

func compareDedupeKeys(a, b dedupeKey) int {
	return cmp.Or(cmp.Compare(a.user, b.user), cmp.Compare(a.key, b.key))
}
