package journal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// FileName is the journal's file name inside the data directory. Tier 1
// uses one growing file; rotation into segments arrives with snapshots in
// Tier 2.
const FileName = "journal.wal"

// ErrFailed means an earlier write or sync failed. After such a failure some
// bytes may or may not be on disk, and only re-reading the file after a
// restart can tell. So the journal refuses every later append instead of
// guessing.
var ErrFailed = errors.New("journal: failed earlier; restart required")

// Hooks are failpoints for the crash harness (tests/crash). Production code
// leaves them nil. Each is called with the Seq being appended, at one
// boundary inside Append. A test hook that calls os.Exit there simulates the
// process dying at exactly that moment.
type Hooks struct {
	BeforeWrite func(seq domain.Seq) // nothing of the frame written yet
	MidWrite    func(seq domain.Seq) // exactly half the frame written: a torn write
	AfterWrite  func(seq domain.Seq) // whole frame written (in the OS cache), not yet fsynced
	AfterSync   func(seq domain.Seq) // fsynced: durable
}

// Options configures Open.
type Options struct {
	Hooks Hooks
}

// Journal appends commands durably to one file. It is used by the engine's
// single owner goroutine only, so it has no lock.
type Journal struct {
	f interface {
		Write([]byte) (int, error)
		Sync() error
		Close() error
	}
	nextSeq domain.Seq
	err     error // sticky: set by the first write or sync failure
	hooks   Hooks
	buf     []byte // reused for every frame: no allocation per append
}

// Open opens the journal at path (creating it if missing), replays every
// valid command through apply in order, repairs a torn tail, and returns a
// journal ready to append the next Seq.
//
// Recovery policy (ADR-0006):
//   - Torn tail (the last frame cut short by a crash): truncate the file to
//     the last complete frame, fsync, continue. The cut-off command was never
//     acknowledged, because acknowledgement waits for fsync.
//   - Anything corrupt: return the error and leave the file UNTOUCHED, so an
//     operator can investigate. Starting would mean trading on a state that
//     might be missing commands.
//   - apply fails: return its error. The log and the code disagree, which
//     needs a human.
func Open(path string, opts Options, apply func(command.Command) error) (*Journal, ScanResult, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, ScanResult{}, fmt.Errorf("journal: %w", err)
	}
	fail := func(res ScanResult, err error) (*Journal, ScanResult, error) {
		_ = f.Close()
		return nil, res, err
	}

	res, err := Scan(f, apply)
	if err != nil {
		return fail(res, err)
	}
	if res.TornBytes > 0 {
		if err := f.Truncate(res.ValidBytes); err != nil {
			return fail(res, fmt.Errorf("journal: truncating torn tail: %w", err))
		}
		if err := f.Sync(); err != nil {
			return fail(res, fmt.Errorf("journal: syncing after truncate: %w", err))
		}
	}
	// Scan read through a buffer, so the file position is wherever reading
	// stopped. Seek to the exact end of the valid frames before appending.
	if _, err := f.Seek(res.ValidBytes, io.SeekStart); err != nil {
		return fail(res, fmt.Errorf("journal: %w", err))
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return fail(res, fmt.Errorf("journal: syncing directory: %w", err))
	}
	return &Journal{f: f, nextSeq: res.LastSeq + 1, hooks: opts.Hooks}, res, nil
}

// Append writes c as one frame and fsyncs it. When it returns nil, the
// command survives a crash of this process. That is the append-and-sync
// contract the engine relies on before applying anything.
//
// c.Seq must be exactly the next Seq. A wrong Seq or an unencodable command
// is refused before anything is written, and the journal stays usable. Any
// WRITE or SYNC error, however, is sticky (see ErrFailed): the bytes may be
// partly on disk, and only a restart can find out.
func (j *Journal) Append(c command.Command) error { return j.AppendBatch([]command.Command{c}) }

// AppendBatch validates and encodes every frame before writing. Normal operation
// uses one write and one sync. A crash can recover a complete prefix of a batch;
// batching does not change the frame format or make commands transactional.
func (j *Journal) AppendBatch(cmds []command.Command) error {
	if j.err != nil {
		return fmt.Errorf("%w: %w", ErrFailed, j.err)
	}
	if len(cmds) == 0 {
		return nil
	}
	buf := j.buf[:0]
	var ends []int
	hooked := j.hooks.BeforeWrite != nil || j.hooks.MidWrite != nil || j.hooks.AfterWrite != nil
	for i, c := range cmds {
		if c.Seq != j.nextSeq+domain.Seq(i) {
			return fmt.Errorf("journal: append seq %d, expected %d", c.Seq, j.nextSeq+domain.Seq(i))
		}
		var err error
		buf, err = appendFrame(buf, c)
		if err != nil {
			return err
		}
		if hooked {
			ends = append(ends, len(buf))
		}
	}
	j.buf = buf
	if hooked {
		start := 0
		for i, c := range cmds {
			frame := buf[start:ends[i]]
			call(j.hooks.BeforeWrite, c.Seq)
			if j.hooks.MidWrite != nil {
				half := len(frame) / 2
				if err := j.write(frame[:half]); err != nil {
					return err
				}
				j.hooks.MidWrite(c.Seq)
				frame = frame[half:]
			}
			if err := j.write(frame); err != nil {
				return err
			}
			call(j.hooks.AfterWrite, c.Seq)
			start = ends[i]
		}
	} else if err := j.write(buf); err != nil {
		return err
	}
	if err := j.f.Sync(); err != nil {
		return j.fail(err)
	}
	for _, c := range cmds {
		call(j.hooks.AfterSync, c.Seq)
	}
	j.nextSeq += domain.Seq(len(cmds))
	return nil
}

// write writes b completely. os.File.Write returns an error whenever it
// writes fewer bytes than asked (a "short write"), so no retry loop is
// needed here, but a short write still counts as a failure.
func (j *Journal) write(b []byte) error {
	if n, err := j.f.Write(b); err != nil {
		return j.fail(err)
	} else if n != len(b) {
		return j.fail(io.ErrShortWrite)
	}
	return nil
}

func (j *Journal) fail(err error) error {
	j.err = err
	return fmt.Errorf("journal: %w", err)
}

// Close closes the file. Every successful Append was already synced, so
// Close has nothing left to flush.
func (j *Journal) Close() error { return j.f.Close() }

func call(hook func(domain.Seq), seq domain.Seq) {
	if hook != nil {
		hook(seq)
	}
}

// syncDir makes a newly created file's directory entry durable. POSIX file
// systems need an fsync of the DIRECTORY for that; without it, a power loss
// can lose the new file's name even though its data was synced. Windows has
// no equivalent call (directory metadata is persisted by NTFS's own
// journal), so it is skipped there.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
