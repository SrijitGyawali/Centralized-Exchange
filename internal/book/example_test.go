package book_test

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Example is the book's acceptance scenario: add five orders, cancel some,
// print the expected book, and reject invalid units without panicking.
//
// This file uses package book_test (note the _test suffix): a separate
// "black-box" test package that can only use book's exported API, exactly
// like real callers. `go test` runs each Example and compares what it prints
// with its Output comment, and `go doc` shows it as documentation.
func Example() {
	b := book.New()

	for _, o := range []book.Order{
		{ID: 1, User: 7, Side: domain.Buy, Price: 60000, Qty: 1000, Seq: 1},  // bid 0.1 BTC at 60,000
		{ID: 2, User: 8, Side: domain.Buy, Price: 60000, Qty: 2500, Seq: 2},  // bid 0.25 at 60,000, queued behind #1
		{ID: 3, User: 9, Side: domain.Buy, Price: 59900, Qty: 5000, Seq: 3},  // bid 0.5 at 59,900
		{ID: 4, User: 7, Side: domain.Sell, Price: 60100, Qty: 3000, Seq: 4}, // ask 0.3 at 60,100
		{ID: 5, User: 8, Side: domain.Sell, Price: 60200, Qty: 1500, Seq: 5}, // ask 0.15 at 60,200
	} {
		if _, err := b.Place(o, domain.GTC); err != nil {
			fmt.Println("place failed:", err)
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
	_, err := b.Place(book.Order{ID: 6, User: 9, Side: domain.Sell, Price: 0, Qty: 1000, Seq: 6}, domain.GTC)
	fmt.Println(err)
	_, err = b.Cancel(1)
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

// ExampleBook_Place shows matching: a buyer sweeps two price levels, pays
// each maker's own price (never more than its limit), and partially fills
// the second maker, which keeps the rest of its order in the book.
func ExampleBook_Place() {
	b := book.New()
	for _, o := range []book.Order{
		{ID: 1, User: 7, Side: domain.Sell, Price: 60100, Qty: 1000, Seq: 1}, // 0.1 BTC at 60,100
		{ID: 2, User: 8, Side: domain.Sell, Price: 60200, Qty: 2000, Seq: 2}, // 0.2 BTC at 60,200
	} {
		if _, err := b.Place(o, domain.GTC); err != nil {
			fmt.Println("place failed:", err)
			return
		}
	}

	// Buy 0.25 BTC, paying at most 60,200.
	res, err := b.Place(book.Order{ID: 3, User: 9, Side: domain.Buy, Price: 60200, Qty: 2500, Seq: 3}, domain.GTC)
	if err != nil {
		fmt.Println("place failed:", err)
		return
	}
	for _, f := range res.Fills {
		fmt.Printf("fill %s BTC at %s from order %d (maker has %s left)\n",
			domain.FormatQty(f.Qty), domain.FormatPrice(f.Price), f.Maker, domain.FormatQty(f.MakerRemaining))
	}
	fmt.Println(res.Outcome)
	fmt.Print(b.Depth(0))

	// Output:
	// fill 0.1000 BTC at 60100 from order 1 (maker has 0.0000 left)
	// fill 0.1500 BTC at 60200 from order 2 (maker has 0.0500 left)
	// filled
	// ASK    60200    0.0500   1
	// ----- no spread (a side is empty) -----
}
