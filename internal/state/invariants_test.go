package state

import (
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// TestCheckInvariantsCatchesCorruption tests the tester: each case takes a
// healthy state and breaks ONE money rule directly in its internals, and
// CheckInvariants must name the problem. Every other state test trusts this
// checker, so it has to be able to fail.
func TestCheckInvariantsCatchesCorruption(t *testing.T) {
	healthy := func(t *testing.T) (*State, domain.OrderID) {
		h := newHarness(t)
		h.fund(alice, domain.USDT, usdt(10_000))
		h.fund(bob, domain.BTC, domain.LotsPerBTC)
		id := h.do(alice, buy(60_000, 1_000, domain.GTC)).Order // Alice reserves 6,000
		h.do(bob, sell(61_000, 1_000, domain.GTC))
		return h.s, id
	}
	key := func(u domain.UserID, a domain.Asset, b Bucket) balanceKey { return balanceKey{u, a, b} }

	tests := []struct {
		name    string
		corrupt func(s *State, id domain.OrderID)
		wantMsg string
	}{
		{"money from nothing", func(s *State, _ domain.OrderID) {
			s.balances[key(alice, domain.USDT, Available)] += 1
		}, "does not balance"},
		{"negative user balance", func(s *State, _ domain.OrderID) {
			s.balances[key(alice, domain.USDT, Available)] = -5                   // was 4,000
			s.balances[key(SystemUser, domain.USDT, Issuance)] += usdt(4_000) + 5 // keep the sum at zero
		}, "negative"},
		{"zero balance stored", func(s *State, _ domain.OrderID) {
			s.balances[key(bob, domain.USDT, Available)] = 0
		}, "zero balance"},
		{"user holds issuance", func(s *State, _ domain.OrderID) {
			s.balances[key(alice, domain.USDT, Issuance)] = -1
			s.balances[key(alice, domain.USDT, Available)] += 1
		}, "issuance account"},
		{"reserved for nothing", func(s *State, _ domain.OrderID) {
			s.balances[key(alice, domain.USDT, Reserved)] += 100
			s.balances[key(alice, domain.USDT, Available)] -= 100
		}, "reserved"},
		{"order reservation wrong", func(s *State, id domain.OrderID) {
			o := s.open[id]
			o.reserved -= 100
			s.open[id] = o
		}, "requires"},
		{"open order missing from book", func(s *State, id domain.OrderID) {
			if _, err := s.book.Cancel(id); err != nil { // behind State's back
				panic(err)
			}
		}, "open orders"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, id := healthy(t)
			tc.corrupt(s, id)
			err := s.CheckInvariants()
			if err == nil {
				t.Fatal("CheckInvariants() = nil on a corrupted state")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("CheckInvariants() = %q, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}
