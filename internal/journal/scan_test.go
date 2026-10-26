package journal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// --- helpers ---------------------------------------------------------------

// sequenced returns the sample commands numbered Seq 1, 2, 3, ...
func sequenced(n int) []command.Command {
	samples := sampleCommands()
	out := make([]command.Command, n)
	for i := range out {
		out[i] = samples[i%len(samples)]
		out[i].Seq = domain.Seq(i + 1)
	}
	return out
}

func encodeFrames(t testing.TB, cmds []command.Command) []byte {
	t.Helper()
	var b []byte
	for _, c := range cmds {
		var err error
		if b, err = appendFrame(b, c); err != nil {
			t.Fatalf("appendFrame(%#v): %v", c, err)
		}
	}
	return b
}

func scanBytes(data []byte) ([]command.Command, ScanResult, error) {
	var got []command.Command
	res, err := Scan(bytes.NewReader(data), func(c command.Command) error {
		got = append(got, c)
		return nil
	})
	return got, res, err
}

// --- the happy path --------------------------------------------------------

func TestScanEmpty(t *testing.T) {
	got, res, err := scanBytes(nil)
	if err != nil || len(got) != 0 || res != (ScanResult{}) {
		t.Errorf("empty journal: %v, %+v, %v; want nothing and no error", got, res, err)
	}
}

func TestScanRoundTrip(t *testing.T) {
	want := sequenced(12)
	data := encodeFrames(t, want)

	got, res, err := scanBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("scanned %d commands, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("command %d: got %#v, want %#v", i, got[i], want[i])
		}
	}
	if res.Frames != 12 || res.LastSeq != 12 || res.ValidBytes != int64(len(data)) || res.TornBytes != 0 {
		t.Errorf("result = %+v", res)
	}
}

// TestFrameLayout checks every header field where the format says it is,
// and both checksums against an independent CRC-32C computation.
func TestFrameLayout(t *testing.T) {
	c := command.Command{Seq: 5, User: 3, Key: "c", Body: command.CancelOrder{Order: 42}}
	f := encodeFrames(t, []command.Command{c})
	payload, _ := appendCommand(nil, c)
	le := binary.LittleEndian
	table := crc32.MakeTable(crc32.Castagnoli)

	switch {
	case len(f) != headerSize+len(payload)+trailerSize:
		t.Fatalf("frame is %d bytes, want %d", len(f), headerSize+len(payload)+trailerSize)
	case f[0] != 'C' || f[1] != 'X':
		t.Errorf("magic = %q, want \"CX\"", f[:2])
	case f[2] != version || f[3] != 0:
		t.Errorf("version/flags = %d/%d, want %d/0", f[2], f[3], version)
	case le.Uint32(f[4:8]) != uint32(len(payload)):
		t.Errorf("length = %d, want %d", le.Uint32(f[4:8]), len(payload))
	case le.Uint64(f[8:16]) != 5:
		t.Errorf("seq = %d, want 5", le.Uint64(f[8:16]))
	case le.Uint32(f[16:20]) != crc32.Checksum(f[:16], table):
		t.Error("header checksum does not cover bytes 0..15")
	case !bytes.Equal(f[20:20+len(payload)], payload):
		t.Error("payload not stored verbatim after the header")
	case le.Uint32(f[len(f)-4:]) != crc32.Checksum(payload, table):
		t.Error("payload checksum does not cover the payload")
	}
}

// --- a torn tail: a crash in the middle of writing the LAST frame ----------

// TestTornTailAtEveryCut cuts the journal at every byte inside its last
// frame. Each time, Scan must keep exactly the complete frames before it
// and report the leftover bytes as a torn tail, not as an error.
func TestTornTailAtEveryCut(t *testing.T) {
	cmds := sequenced(3)
	data := encodeFrames(t, cmds)
	complete := len(encodeFrames(t, cmds[:2]))

	for cut := complete + 1; cut < len(data); cut++ {
		got, res, err := scanBytes(data[:cut])
		if err != nil {
			t.Fatalf("cut at %d: %v; a torn tail is not corruption", cut, err)
		}
		if len(got) != 2 || res.ValidBytes != int64(complete) || res.TornBytes != int64(cut-complete) {
			t.Fatalf("cut at %d: %d commands, %+v; want 2 commands, valid %d, torn %d",
				cut, len(got), res, complete, cut-complete)
		}
	}
}

// --- corruption: a COMPLETE frame that is wrong --------------------------

// TestCorruptionAtEveryByte flips each byte of a 3-frame journal in turn.
// Every single flip must be reported as ErrCorrupt. None may be silently
// skipped or mistaken for a torn tail, and no command at or after the
// damaged frame may be handed to the caller.
func TestCorruptionAtEveryByte(t *testing.T) {
	cmds := sequenced(3)
	data := encodeFrames(t, cmds)
	frameEnd := []int{len(encodeFrames(t, cmds[:1])), len(encodeFrames(t, cmds[:2])), len(data)}

	for i := range data {
		bad := bytes.Clone(data)
		bad[i] ^= 0xFF
		got, _, err := scanBytes(bad)
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("byte %d flipped: error = %v, want ErrCorrupt", i, err)
		}
		damaged := 0
		for damaged < len(frameEnd) && i >= frameEnd[damaged] {
			damaged++
		}
		if len(got) != damaged {
			t.Fatalf("byte %d flipped (frame %d): %d commands were applied, want only the %d before it", i, damaged+1, len(got), damaged)
		}
	}
}

// A partial header that does not even start like our magic is not "our
// write, cut short". It is foreign bytes, so it is corruption.
func TestPartialHeaderMustLookLikeOurs(t *testing.T) {
	data := encodeFrames(t, sequenced(2))
	if _, res, err := scanBytes(append(bytes.Clone(data), 'C')); err != nil || res.TornBytes != 1 {
		t.Errorf("\"C\" after the frames: %+v, %v; want a 1-byte torn tail", res, err)
	}
	if _, _, err := scanBytes(append(bytes.Clone(data), 'Z')); !errors.Is(err, ErrCorrupt) {
		t.Errorf("\"Z\" after the frames: error = %v, want ErrCorrupt", err)
	}
}

// reframe rewrites one header field of a single frame and recomputes the
// header checksum, building frames that are VALID by checksum but wrong in
// meaning. The checksum alone would not catch these cases.
func reframe(t *testing.T, c command.Command, edit func(h []byte)) []byte {
	t.Helper()
	f := encodeFrames(t, []command.Command{c})
	edit(f[:16])
	binary.LittleEndian.PutUint32(f[16:20], crc32.Checksum(f[:16], crc32.MakeTable(crc32.Castagnoli)))
	return f
}

func TestUnknownVersionIsRefused(t *testing.T) {
	f := reframe(t, sequenced(1)[0], func(h []byte) { h[2] = version + 1 })
	if _, _, err := scanBytes(f); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("error = %v, want ErrUnsupportedVersion", err)
	}
}

// The subtle case: a header claiming a payload larger than any real frame,
// at the very end of the file. Read naively, the missing bytes would look
// like a torn tail and the frame would be silently dropped. A length over
// the maximum can never come from our own (cut-short) write, so it is
// corruption.
func TestOversizedLengthIsCorruptNotTorn(t *testing.T) {
	f := reframe(t, sequenced(1)[0], func(h []byte) { binary.LittleEndian.PutUint32(h[4:8], maxPayload+1) })
	if _, _, err := scanBytes(f[:headerSize]); !errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

func TestSequenceMustBeContiguousFromOne(t *testing.T) {
	cmds := sequenced(3)
	cmds[2].Seq = 4 // 1, 2, 4: a gap
	if _, _, err := scanBytes(encodeFrames(t, cmds)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("gap: error = %v, want ErrCorrupt", err)
	}
	first := sequenced(1)
	first[0].Seq = 2
	if _, _, err := scanBytes(encodeFrames(t, first)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("first seq 2: error = %v, want ErrCorrupt", err)
	}
}

func TestUndecodablePayloadIsCorrupt(t *testing.T) {
	// Valid checksums around a payload with an unknown body kind.
	f := encodeFrames(t, sequenced(1))
	f[headerSize] = 0x7f
	binary.LittleEndian.PutUint32(f[len(f)-4:], crc32.Checksum(f[headerSize:len(f)-4], crc32.MakeTable(crc32.Castagnoli)))
	if _, _, err := scanBytes(f); !errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

func TestScanStopsAtTheFirstApplyError(t *testing.T) {
	boom := errors.New("boom")
	calls := 0
	res, err := Scan(bytes.NewReader(encodeFrames(t, sequenced(5))), func(c command.Command) error {
		calls++
		if c.Seq == 2 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) || calls != 2 || res.Frames != 1 {
		t.Errorf("err %v, calls %d, frames %d; want boom after 2 calls with 1 frame counted", err, calls, res.Frames)
	}
}

// FuzzScan: arbitrary bytes must never panic. When Scan succeeds, every byte
// is accounted for (valid prefix + torn tail = whole input), and the valid
// prefix is exactly the encoding of the commands it returned.
func FuzzScan(f *testing.F) {
	valid := encodeFrames(f, sequenced(4))
	f.Add(valid)
	f.Add(valid[:len(valid)-3])
	f.Add(append(bytes.Clone(valid), 'C', 'X'))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		got, res, err := scanBytes(data)
		if err != nil {
			return
		}
		if res.ValidBytes+res.TornBytes != int64(len(data)) {
			t.Fatalf("valid %d + torn %d != input %d", res.ValidBytes, res.TornBytes, len(data))
		}
		if again := encodeFrames(t, got); !bytes.Equal(again, data[:res.ValidBytes]) {
			t.Fatalf("valid prefix is not the encoding of the returned commands")
		}
	})
}

// TestScanFromResumesMidJournal: reading the first frames, then resuming at
// the byte offset reached with the next Seq, must see exactly the rest, as a
// projector following a growing journal does.
func TestScanFromResumesMidJournal(t *testing.T) {
	cmds := sequenced(5)
	data := encodeFrames(t, cmds)
	first, res, err := scanBytes(encodeFrames(t, cmds[:2]))
	if err != nil || len(first) != 2 {
		t.Fatalf("first part: %v, %v", first, err)
	}

	var rest []command.Command
	res2, err := ScanFrom(bytes.NewReader(data[res.ValidBytes:]), res.LastSeq+1, func(c command.Command) error {
		rest = append(rest, c)
		return nil
	})
	if err != nil || len(rest) != 3 || rest[0].Seq != 3 || res2.LastSeq != 5 {
		t.Fatalf("resumed scan: %d commands, %+v, %v; want seqs 3..5", len(rest), res2, err)
	}
	if res2.ValidBytes != int64(len(data))-res.ValidBytes {
		t.Errorf("ValidBytes = %d, want the bytes after the offset", res2.ValidBytes)
	}

	// Resuming with the wrong expected Seq is a gap, i.e. corruption.
	if _, err := ScanFrom(bytes.NewReader(data[res.ValidBytes:]), 4, nil); !errors.Is(err, ErrCorrupt) {
		t.Errorf("resume expecting seq 4 at seq 3: error = %v, want ErrCorrupt", err)
	}
}
