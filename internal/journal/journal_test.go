package journal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

func collect(into *[]command.Command) func(command.Command) error {
	return func(c command.Command) error { *into = append(*into, c); return nil }
}

// open opens (or creates) a journal and closes it when the test ends.
func open(t *testing.T, path string, opts Options) (*Journal, []command.Command, ScanResult) {
	t.Helper()
	var got []command.Command
	j, res, err := Open(path, opts, collect(&got))
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, got, res
}

func appendAll(t *testing.T, j *Journal, cmds []command.Command) {
	t.Helper()
	for _, c := range cmds {
		if err := j.Append(c); err != nil {
			t.Fatalf("Append(seq %d): %v", c.Seq, err)
		}
	}
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestOpenCreatesAnEmptyJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	_, got, res := open(t, path, Options{})
	if len(got) != 0 || res != (ScanResult{}) || size(t, path) != 0 {
		t.Errorf("fresh journal: %d commands, %+v, size %d", len(got), res, size(t, path))
	}
}

func TestAppendSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	j, _, _ := open(t, path, Options{})
	want := sequenced(5)
	appendAll(t, j, want)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	j2, got, res := open(t, path, Options{})
	if !slices.Equal(got, want) || res.LastSeq != 5 || res.TornBytes != 0 {
		t.Fatalf("after reopen: %d commands, %+v", len(got), res)
	}
	// It continues exactly where it left off.
	next := sequenced(6)[5]
	if err := j2.Append(next); err != nil {
		t.Errorf("Append(seq 6) after reopen: %v", err)
	}
}

func TestAppendRequiresTheNextSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	j, _, _ := open(t, path, Options{})
	c := sequenced(2)

	if err := j.Append(c[1]); err == nil || errors.Is(err, ErrFailed) {
		t.Fatalf("Append(seq 2) on an empty journal: error = %v, want a plain refusal", err)
	}
	if size(t, path) != 0 {
		t.Fatal("a refused append wrote bytes")
	}
	if err := j.Append(c[0]); err != nil {
		t.Errorf("Append(seq 1) after a refusal: %v; a refusal must not break the journal", err)
	}
}

// TestTornTailIsRepairedOnOpen simulates a crash in the middle of writing
// the last frame, at every possible byte. Open must keep the complete
// frames, cut the file back to them, and accept the next append in place.
func TestTornTailIsRepairedOnOpen(t *testing.T) {
	cmds := sequenced(3)
	full := encodeFrames(t, cmds)
	complete := int64(len(encodeFrames(t, cmds[:2])))

	for cut := complete + 1; cut < int64(len(full)); cut++ {
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.WriteFile(path, full[:cut], 0o644); err != nil {
			t.Fatal(err)
		}

		j, got, res := open(t, path, Options{})
		if len(got) != 2 || res.TornBytes != cut-complete || size(t, path) != complete {
			t.Fatalf("cut %d: %d commands, %+v, file size %d; want 2, torn %d, size %d",
				cut, len(got), res, size(t, path), cut-complete, complete)
		}
		if err := j.Append(cmds[2]); err != nil { // the lost command can be written again
			t.Fatalf("cut %d: Append after repair: %v", cut, err)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		if _, got, res := open(t, path, Options{}); len(got) != 3 || res.TornBytes != 0 {
			t.Fatalf("cut %d: after repair and re-append: %d commands, %+v", cut, len(got), res)
		}
	}
}

// TestCorruptJournalIsRefusedAndLeftAlone: a damaged COMPLETE frame must
// stop Open with ErrCorrupt, and Open must not "repair" (truncate) the file.
// The evidence stays intact for an operator to investigate.
func TestCorruptJournalIsRefusedAndLeftAlone(t *testing.T) {
	data := encodeFrames(t, sequenced(3))
	data[len(encodeFrames(t, sequenced(1)))+headerSize] ^= 0x01 // a payload bit of frame 2
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	j, _, err := Open(path, Options{}, nil)
	if !errors.Is(err, ErrCorrupt) || j != nil {
		t.Fatalf("Open: %v, %v; want ErrCorrupt and no journal", j, err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, data) {
		t.Error("Open modified a corrupt journal; it must leave it untouched")
	}
}

// TestFailureIsSticky: after any write or sync error, some bytes may or may
// not have reached the disk. The journal cannot know, so it refuses every
// later append until a restart re-reads the file.
func TestFailureIsSticky(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	j, _, _ := open(t, path, Options{})
	cmds := sequenced(2)

	_ = j.f.Close() // simulate the disk going away underneath the journal
	if err := j.Append(cmds[0]); err == nil {
		t.Fatal("Append to a closed file succeeded")
	}
	if err := j.Append(cmds[0]); !errors.Is(err, ErrFailed) {
		t.Errorf("second Append after a failure: error = %v, want ErrFailed", err)
	}
}

// TestHooksFireInOrder checks the crash points the crash harness relies on,
// and how much of the frame is on disk at each one.
func TestHooksFireInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	c := sequenced(1)[0]
	frameLen := int64(len(encodeFrames(t, []command.Command{c})))

	var events []string
	var sizes []int64
	at := func(name string) func(domain.Seq) {
		return func(seq domain.Seq) {
			if seq != 1 {
				t.Errorf("%s hook got seq %d, want 1", name, seq)
			}
			events = append(events, name)
			sizes = append(sizes, size(t, path))
		}
	}
	j, _, _ := open(t, path, Options{Hooks: Hooks{
		BeforeWrite: at("before-write"), MidWrite: at("mid-write"),
		AfterWrite: at("after-write"), AfterSync: at("after-sync"),
	}})
	appendAll(t, j, []command.Command{c})

	if want := []string{"before-write", "mid-write", "after-write", "after-sync"}; !slices.Equal(events, want) {
		t.Fatalf("hooks fired %v, want %v", events, want)
	}
	if want := []int64{0, frameLen / 2, frameLen, frameLen}; !slices.Equal(sizes, want) {
		t.Errorf("file sizes at the hooks = %v, want %v (half the frame at mid-write)", sizes, want)
	}
}
