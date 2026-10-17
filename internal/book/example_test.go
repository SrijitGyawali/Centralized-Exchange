package book_test

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Example is the week-1 gate from the plan: add five orders, cancel some,
// print the expected book, and reject invalid units without panicking.
//
// This file uses package book_test (note the _test suffix): a separate
// "black-box" test package that can only use book's exported API, exactly
// like real callers. `go test` runs Example and compares what it prints with
// the Output comment at the end, and `go doc` shows it as documentation.
func Example() {
	b := book.New()

	for _, o := range []book.Order{
		{ID: 1, User: 7, Side: domain.Buy, Price: 60000, Qty: 1000, Seq: 1},  // bid 0.1 BTC at 60,000
		{ID: 2, User: 8, Side: domain.Buy, Price: 60000, Qty: 2500, Seq: 2},  // bid 0.25 at 60,000, queued behind #1
		{ID: 3, User: 9, Side: domain.Buy, Price: 59900, Qty: 5000, Seq: 3},  // bid 0.5 at 59,900
		{ID: 4, User: 7, Side: domain.Sell, Price: 60100, Qty: 3000, Seq: 4}, // ask 0.3 at 60,100
		{ID: 5, User: 8, Side: domain.Sell, Price: 60200, Qty: 1500, Seq: 5}, // ask 0.15 at 60,200
	} {
		if err := b.Add(o); err != nil {
			fmt.Println("add failed:", err)
			return
		}
	}
	fmt.Print(b.Depth(0))

	// Cancel #1, the front of the 60,000 queue, so #2 moves up. Cancel #5,
	// the only order at 60,200, so that level disappears.
	for _, id := range []domain.OrderID{1, 5} {
		if _, err := b.Cancel(id); err != nil {
			fmt.Println("cancel failed:", err)
			return
		}
	}
	fmt.Println("after cancelling 1 and 5:")
	fmt.Print(b.Depth(0))

	// Bad input comes back as an error value, never a panic.
	fmt.Println(b.Add(book.Order{ID: 6, User: 9, Side: domain.Sell, Price: 0, Qty: 1000, Seq: 6}))
	_, err := b.Cancel(1)
	fmt.Println(err)

	// Output:
	// ASK    60200    0.1500   1
	// ASK    60100    0.3000   1
	// ----- spread 100 -----
	// BID    60000    0.3500   2
	// BID    59900    0.5000   1
	// after cancelling 1 and 5:
	// ASK    60100    0.3000   1
	// ----- spread 100 -----
	// BID    60000    0.2500   1
	// BID    59900    0.5000   1
	// invalid order: price 0: value out of range
	// unknown order id: 1
}
