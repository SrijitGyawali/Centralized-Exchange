package state

import (
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

func TestJournalBalanced(t *testing.T) {
	p := func(u domain.UserID, b Bucket, a domain.Asset, amt int64) Posting {
		return Posting{Account{u, b}, a, amt}
	}
	tests := []struct {
		name     string
		postings []Posting
		want     bool
	}{
		{"settlement: each asset nets to zero", []Posting{
			p(1, Reserved, domain.USDT, -600), p(2, Available, domain.USDT, 600),
			p(2, Reserved, domain.BTC, -1), p(1, Available, domain.BTC, 1),
		}, true},
		{"no postings", nil, true},
		{"money created", []Posting{p(1, Available, domain.USDT, 5)}, false},
		{"one side short", []Posting{p(1, Available, domain.USDT, -5), p(2, Available, domain.USDT, 4)}, false},
		// The rule is per asset: 5 BTC-units out and 5 USDT-units in is NOT
		// balanced, even though the raw numbers cancel.
		{"assets cannot offset each other", []Posting{p(1, Available, domain.USDT, 5), p(1, Available, domain.BTC, -5)}, false},
		{"invalid asset", []Posting{p(1, Available, 0, 0)}, false},
	}
	for _, tc := range tests {
		if got := (Journal{Postings: tc.postings}).Balanced(); got != tc.want {
			t.Errorf("%s: Balanced() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The guards in tx.post can only fire through a bug, so they are tested
// directly: each must panic rather than let wrong money into the state.
func TestPostRefusesBadPostings(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(s *State)
		postings []Posting
		wantMsg  string
	}{
		{"unbalanced journal", nil,
			[]Posting{{Account{alice, Available}, domain.USDT, 5}},
			"unbalanced"},
		{"user balance would go negative", nil,
			move(alice, domain.USDT, Available, Reserved, 1), // Alice has nothing to reserve
			"negative"},
		{"balance would overflow", func(s *State) {
			s.balances[balanceKey{alice, domain.USDT, Available}] = 1<<63 - 1
			s.balances[balanceKey{bob, domain.USDT, Available}] = 1
		},
			[]Posting{{Account{alice, Available}, domain.USDT, 1}, {Account{bob, Available}, domain.USDT, -1}},
			"overflow"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			if tc.setup != nil {
				tc.setup(s)
			}
			defer func() {
				r := recover() // turns the panic back into a value the test can inspect
				if r == nil {
					t.Fatal("post accepted a bad journal instead of panicking")
				}
				if msg, _ := r.(string); !strings.Contains(msg, tc.wantMsg) {
					t.Errorf("panic %q, want it to mention %q", r, tc.wantMsg)
				}
			}()
			(&tx{s: s, seq: 1}).post(Settlement, 0, 0, tc.postings...)
		})
	}
}

func TestLedgerNames(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{Available.String(), "available"},
		{Reserved.String(), "reserved"},
		{Issuance.String(), "issuance"},
		{Bucket(9).String(), "Bucket(9)"},
		{Funding.String(), "funding"},
		{Reservation.String(), "reservation"},
		{Settlement.String(), "settlement"},
		{Release.String(), "release"},
		{JournalKind(9).String(), "JournalKind(9)"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}
