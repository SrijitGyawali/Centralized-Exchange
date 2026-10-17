package book

import (
	"cmp"
	"slices"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// ladder is one side of the book: its price levels sorted best-first.
//
//	bids (desc = true):   60000, 59900, 59800 …   highest price is best
//	asks (desc = false):  60100, 60200, 60300 …   lowest price is best
//
// Why a sorted slice? It is the simplest structure that keeps depth in order
// for free, and levels[0] is always the best price.
//
//	find a price:      binary search          O(log P)
//	add a new level:   shift later elements   O(P)
//	delete a level:    shift later elements   O(P)
//
// where P is the number of price levels on this side, usually small because
// prices cluster near the spread. A heap or tree could be faster for huge
// books, but the plan says to measure before reaching for one (page 7).
type ladder struct {
	levels []*level
	desc   bool
}

// search returns the index of the level for price p and true, or, if there
// is none, the index where it would have to be inserted to keep the slice
// sorted, and false.
//
// slices.BinarySearchFunc expects ascending order under its compare
// function. For bids we reverse the comparison, so "ascending" means
// "highest price first".
func (ld *ladder) search(p domain.Price) (int, bool) {
	return slices.BinarySearchFunc(ld.levels, p, func(l *level, target domain.Price) int {
		if ld.desc {
			return cmp.Compare(target, l.price)
		}
		return cmp.Compare(l.price, target)
	})
}

// best returns the best level, or nil if this side is empty.
func (ld *ladder) best() *level {
	if len(ld.levels) == 0 {
		return nil
	}
	return ld.levels[0]
}

// insertAt creates an empty level for price p at index i (from search) and
// returns it.
func (ld *ladder) insertAt(i int, p domain.Price) *level {
	l := &level{price: p}
	ld.levels = slices.Insert(ld.levels, i, l)
	return l
}

// removeLevel deletes the (now empty) level l from the ladder.
//
// It panics if l is missing. That can only happen through a bug in this
// package, never through user input, and continuing with a corrupted book
// would be worse than stopping. Go's convention: return errors for things
// callers can cause; panic for "this cannot happen".
func (ld *ladder) removeLevel(l *level) {
	i, found := ld.search(l.price)
	if !found || ld.levels[i] != l {
		panic("book: removing a price level that is not in the ladder")
	}
	// slices.Delete also clears the freed slot at the end, so the removed
	// *level can be garbage collected.
	ld.levels = slices.Delete(ld.levels, i, i+1)
}
