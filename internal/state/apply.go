package state

import (
	"errors"
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
)

// Effects is everything one command did: the Result for the client, and the
// ledger Journals it posted, in order. A rejected or duplicate command has
// no journals.
type Effects struct {
	Result   command.Result
	Journals []Journal
}

// ErrSeqNotIncreasing means the caller (the engine) passed commands out of
// order. It is a bug in the caller, not a business outcome, so it is a Go
// error and the state is left untouched.
var ErrSeqNotIncreasing = errors.New("command sequence not increasing")

// Apply executes one sequenced command: the single, deterministic state
// transition (ADR-0001).
//
// The steps, in order:
//
//  1. Sequencing: cmd.Seq must be higher than the last one applied, and the
//     body must be one of the known kinds. Otherwise return a Go error and
//     change nothing. These are caller bugs.
//  2. Idempotency key: it must be 1..MaxKeyLen bytes. Otherwise reject
//     without remembering anything (there is no valid key to remember).
//  3. Deduplication by (user, key):
//     - seen before with the SAME body: return the stored result again,
//     marked Duplicate. Nothing is executed and no money moves.
//     - seen before with a DIFFERENT body: reject with RejectKeyConflict.
//     The key keeps its original meaning.
//  4. Execute the body (fund, place or cancel). Business rejections such as
//     insufficient funds are normal results.
//  5. Remember the result under (user, key), whether accepted or rejected.
//     A rejection is a decision too, and a retry must see the same one.
//
// Apply is pure in the sense the whole design depends on: no clock, no
// randomness, no I/O, no dependence on map iteration order. The same
// commands in the same order always produce the same results, journals and
// state, which is what makes crash recovery by replay (week 4) possible.
func (s *State) Apply(cmd command.Command) (Effects, error) {
	if cmd.Seq <= s.lastSeq {
		return Effects{}, fmt.Errorf("%w: got %d, last was %d", ErrSeqNotIncreasing, cmd.Seq, s.lastSeq)
	}
	switch cmd.Body.(type) {
	case command.Fund, command.PlaceOrder, command.CancelOrder:
	default:
		return Effects{}, fmt.Errorf("state: unknown command body %T", cmd.Body)
	}
	s.lastSeq = cmd.Seq // every sequenced command consumes its Seq, whatever happens next

	if len(cmd.Key) == 0 || len(cmd.Key) > command.MaxKeyLen {
		r := reject(command.RejectInvalid, "idempotency key must be 1..%d bytes, got %d", command.MaxKeyLen, len(cmd.Key))
		r.Seq = cmd.Seq
		return Effects{Result: r}, nil
	}

	dk := dedupeKey{cmd.User, cmd.Key}
	if prev, seen := s.dedupe[dk]; seen {
		if prev.body == cmd.Body { // interface ==: same dynamic type AND same field values
			r := prev.result.Clone() // a copy: the caller must not be able to edit the stored one
			r.Duplicate = true
			return Effects{Result: r}, nil
		}
		r := reject(command.RejectKeyConflict, "key %q was already used by seq %d for a different request", cmd.Key, prev.result.Seq)
		r.Seq = cmd.Seq
		return Effects{Result: r}, nil
	}

	t := &tx{s: s, seq: cmd.Seq}
	var res command.Result
	switch b := cmd.Body.(type) {
	case command.Fund:
		res = s.fund(t, b)
	case command.PlaceOrder:
		res = s.place(t, cmd.User, b)
	case command.CancelOrder:
		res = s.cancel(t, cmd.User, b)
	}
	res.Seq = cmd.Seq

	s.dedupe[dk] = dedupeEntry{body: cmd.Body, result: res.Clone()}
	return Effects{Result: res, Journals: t.journals}, nil
}
