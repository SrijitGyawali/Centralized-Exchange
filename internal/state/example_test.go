package state_test

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// Example follows the money through one trade, as the replay inspector will
// one day: Alice buys 0.1 BTC from Bob. She is willing to pay up to 60,100
// but Bob asks only 60,000, so 10 USDT of her reservation comes back. Then
// her response is "lost" and she retries with the same key: nothing happens
// twice.
func Example() {
	s := state.New()
	var seq domain.Seq
	apply := func(user domain.UserID, key string, body command.Body) state.Effects {
		seq++
		fx, err := s.Apply(command.Command{Seq: seq, User: user, Key: key, Body: body})
		if err != nil {
			panic(err) // only for bad sequencing, which this example never does
		}
		return fx
	}
	const operator, alice, bob = 99, 1, 2

	apply(operator, "fund-alice", command.Fund{To: alice, Asset: domain.USDT, Amount: 10_000 * domain.AtomsPerUSDT})
	apply(operator, "fund-bob", command.Fund{To: bob, Asset: domain.BTC, Amount: 1 * domain.LotsPerBTC})
	apply(bob, "bob-1", command.PlaceOrder{Side: domain.Sell, Price: 60_000, Qty: 1_000, TIF: domain.GTC})

	aliceOrder := command.PlaceOrder{Side: domain.Buy, Price: 60_100, Qty: 1_000, TIF: domain.GTC}
	fx := apply(alice, "alice-1", aliceOrder)

	fmt.Println("alice's order:", fx.Result.Outcome)
	for _, j := range fx.Journals {
		fmt.Printf("journal %d.%d %v\n", j.Seq, j.Index, j.Kind)
		for _, p := range j.Postings {
			sign := "+"
			if p.Amount < 0 {
				sign = ""
			}
			fmt.Printf("  %-4v  user %d  %-9v  %s%s\n", p.Asset, p.Account.User, p.Account.Bucket, sign, p.Asset.Format(p.Amount))
		}
	}

	fmt.Println("balances:")
	for _, u := range []domain.UserID{alice, bob} {
		for _, a := range []domain.Asset{domain.BTC, domain.USDT} {
			avail, reserved := s.Balance(u, a)
			fmt.Printf("  user %d  %-4v  available %s  reserved %s\n", u, a, a.Format(avail), a.Format(reserved))
		}
	}

	retry := apply(alice, "alice-1", aliceOrder) // the "lost response" retry
	fmt.Printf("retry: result of seq %d, %v, duplicate=%v, %d journals\n",
		retry.Result.Seq, retry.Result.Outcome, retry.Result.Duplicate, len(retry.Journals))

	// Output:
	// alice's order: filled
	// journal 4.0 reservation
	//   USDT  user 1  available  -6010.000000
	//   USDT  user 1  reserved   +6010.000000
	// journal 4.1 settlement
	//   USDT  user 1  reserved   -6000.000000
	//   USDT  user 2  available  +6000.000000
	//   BTC   user 2  reserved   -0.1000
	//   BTC   user 1  available  +0.1000
	// journal 4.2 release
	//   USDT  user 1  reserved   -10.000000
	//   USDT  user 1  available  +10.000000
	// balances:
	//   user 1  BTC   available 0.1000  reserved 0.0000
	//   user 1  USDT  available 4000.000000  reserved 0.000000
	//   user 2  BTC   available 0.9000  reserved 0.0000
	//   user 2  USDT  available 6000.000000  reserved 0.000000
	// retry: result of seq 4, filled, duplicate=true, 0 journals
}
