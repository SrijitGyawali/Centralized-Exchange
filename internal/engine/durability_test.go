package engine

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// fakeJournal stands in for the real file-backed journal. Because the engine
// defines the Journal interface it needs (one method), the fake is a dozen
// lines. It records every append, can fail at a chosen Seq, and checks the
// key ordering rule: at append time the command must NOT yet be applied.
type fakeJournal struct {
	mu           sync.Mutex // the test goroutine reads what the Run goroutine writes
	e            *Engine
	failAt       domain.Seq
	cmds         []command.Command
	appliedEarly bool
}

func (f *fakeJournal) Append(c command.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.Seq == f.failAt {
		return errors.New("disk full")
	}
	// Snapshot().Seq is the last APPLIED command. While c is being made
	// durable, it must still be the one before c.
	if f.e.Snapshot().Seq != c.Seq-1 {
		f.appliedEarly = true
	}
	f.cmds = append(f.cmds, c)
	return nil
}

func (f *fakeJournal) appended() ([]command.Command, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cmds), f.appliedEarly
}

func withFakeJournal(t *testing.T, failAt domain.Seq) (*Engine, *fakeJournal) {
	t.Helper()
	fj := &fakeJournal{failAt: failAt}
	e := New(state.New(), Config{Journal: fj})
	fj.e = e // set before Run starts, so no race
	runInBackground(t, e)
	return e, fj
}

// TestEveryCommandIsJournaledBeforeItIsApplied: accepted commands, business
// rejections AND duplicates all reach the journal, in Seq order, before
// they change anything. Replay must reproduce rejections and dedupe
// decisions exactly, so they have to be in the log too.
func TestEveryCommandIsJournaledBeforeItIsApplied(t *testing.T) {
	e, fj := withFakeJournal(t, 0)
	sent := []command.Command{
		{User: operator, Key: "fund", Body: fundAlice(usdt(10_000))},
		{User: alice, Key: "bid", Body: buy(60_000, 1_000, domain.GTC)},
		{User: alice, Key: "too-big", Body: buy(60_000, 100_000, domain.GTC)}, // insufficient funds
		{User: operator, Key: "fund", Body: fundAlice(usdt(10_000))},          // duplicate
		{User: alice, Key: "cancel", Body: command.CancelOrder{Order: 77}},    // unknown order
	}
	for _, c := range sent {
		mustSubmit(t, e, c.User, c.Key, c.Body)
	}

	got, early := fj.appended()
	if len(got) != len(sent) {
		t.Fatalf("journal has %d commands, want all %d", len(got), len(sent))
	}
	for i, c := range got {
		want := sent[i]
		want.Seq = domain.Seq(i + 1)
		if c != want {
			t.Errorf("journal[%d] = %#v, want %#v", i, c, want)
		}
	}
	if early {
		t.Error("a command was applied before the journal accepted it")
	}
}

// TestJournalFailureHaltsBeforeApplying: if the journal cannot confirm a
// write, the command must not take effect, and nothing else may follow.
func TestJournalFailureHaltsBeforeApplying(t *testing.T) {
	e, _ := withFakeJournal(t, 3)
	mustSubmit(t, e, operator, newKey(), fundAlice(1))
	mustSubmit(t, e, operator, newKey(), fundAlice(1))

	_, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1))
	if !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, ErrHalted) {
		t.Fatalf("error = %v; want ErrOutcomeUnknown + ErrHalted: the bytes MAY be on disk", err)
	}
	if e.Err() == nil || !strings.Contains(e.Err().Error(), "disk full") {
		t.Errorf("Err() = %v, want it to report the journal failure", e.Err())
	}
	if seq := e.Snapshot().Seq; seq != 2 {
		t.Errorf("last applied seq = %d, want 2: the failed command must not be applied", seq)
	}
	if _, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1)); !errors.Is(err, ErrHalted) {
		t.Errorf("Submit after the failure: error = %v, want ErrHalted", err)
	}
}

func TestInvalidKeysAreNotAdmitted(t *testing.T) {
	e, fj := withFakeJournal(t, 0)
	for _, key := range []string{"", strings.Repeat("k", command.MaxKeyLen+1)} {
		if _, err := e.Submit(t.Context(), alice, key, fundAlice(1)); !errors.Is(err, ErrInvalidCommand) {
			t.Errorf("key of %d bytes: error = %v, want ErrInvalidCommand", len(key), err)
		}
	}
	if got, _ := fj.appended(); len(got) != 0 {
		t.Errorf("invalid keys reached the journal: %v", got)
	}
}

func TestAfterApplyHookRunsBeforeTheReply(t *testing.T) {
	var mu sync.Mutex
	var hooked []domain.Seq
	e := New(state.New(), Config{Hooks: Hooks{AfterApply: func(seq domain.Seq) {
		mu.Lock()
		hooked = append(hooked, seq)
		mu.Unlock()
	}}})
	runInBackground(t, e)

	r := mustSubmit(t, e, operator, newKey(), fundAlice(1))
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(hooked, []domain.Seq{r.Seq}) {
		t.Errorf("hook saw %v by the time the reply arrived, want [%d]", hooked, r.Seq)
	}
}
