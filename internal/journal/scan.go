package journal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

var (
	// ErrCorrupt: a COMPLETE frame is wrong: bad magic or checksum, a
	// sequence gap, an impossible length, or an undecodable payload.
	// Recovery must stop. Skipping it would silently drop or reorder
	// commands, and with them, money.
	ErrCorrupt = errors.New("journal: corrupt")

	// ErrUnsupportedVersion: a valid frame written in a format version this
	// code does not understand (written by newer software). Stop rather than
	// guess.
	ErrUnsupportedVersion = errors.New("journal: unsupported format version")
)

// ScanResult describes what Scan found.
type ScanResult struct {
	Frames     int        // complete, valid frames
	LastSeq    domain.Seq // Seq of the last valid frame (0 if none)
	ValidBytes int64      // length of the valid prefix
	TornBytes  int64      // bytes after it: an incomplete final frame
}

// Scan reads frames from r in order, checks each completely, and calls fn
// with every valid command (Seq included). It never modifies anything:
// deciding what to do with a torn tail is the caller's job (Open truncates
// it; the replay tool only reports it).
//
// The rules (ADR-0006):
//
//   - The file ends exactly after a frame → clean end.
//   - The file ends INSIDE a frame, after a correct-looking prefix → a torn
//     tail: a crash interrupted the last write. Report its size, no error.
//     Under the process-crash model an interrupted append leaves a prefix of
//     what was written, never garbage.
//   - A COMPLETE frame (or header) that fails any check → ErrCorrupt, or
//     ErrUnsupportedVersion. fn is never called for it or anything after it.
//   - Seqs must run 1, 2, 3, … with no gap.
//
// If fn returns an error, Scan stops and returns it wrapped.
func Scan(r io.Reader, fn func(command.Command) error) (ScanResult, error) {
	return ScanFrom(r, 1, fn)
}

// ScanFrom is Scan for a reader positioned at a frame boundary in the MIDDLE
// of a journal, where the next frame must carry Seq next. A reader that
// follows a growing journal (the SQL projector) uses it to resume where it
// stopped: seek to the byte offset it reached, then ScanFrom the Seq after
// the last one it saw.
//
// LastSeq starts at next-1 and ValidBytes/TornBytes count from the reader's
// starting position. When following a live journal, a torn tail usually just
// means "the writer is in the middle of appending": read again later.
func ScanFrom(r io.Reader, next domain.Seq, fn func(command.Command) error) (ScanResult, error) {
	br := bufio.NewReader(r)
	res := ScanResult{LastSeq: next - 1}
	var hdr [headerSize]byte
	body := make([]byte, maxPayload+trailerSize) // the most a valid frame can need: allocated once

	corrupt := func(format string, args ...any) (ScanResult, error) {
		return res, fmt.Errorf("%w: frame at byte %d: %s", ErrCorrupt, res.ValidBytes, fmt.Sprintf(format, args...))
	}

	for {
		n, err := io.ReadFull(br, hdr[:])
		switch {
		case err == io.EOF:
			return res, nil // clean end, exactly at a frame boundary
		case errors.Is(err, io.ErrUnexpectedEOF):
			// A partial header. Only the magic can be checked; if even that
			// differs, these bytes are not a cut-short write of ours.
			for i := 0; i < n && i < len(magic); i++ {
				if hdr[i] != magic[i] {
					return corrupt("trailing bytes do not start with the frame magic")
				}
			}
			res.TornBytes = int64(n)
			return res, nil
		case err != nil:
			return res, fmt.Errorf("journal: reading: %w", err)
		}

		if hdr[0] != magic[0] || hdr[1] != magic[1] {
			return corrupt("bad magic %q", hdr[:2])
		}
		if crc32.Checksum(hdr[:16], castagnoli) != binary.LittleEndian.Uint32(hdr[16:20]) {
			return corrupt("header checksum mismatch")
		}
		if hdr[2] != version {
			return res, fmt.Errorf("%w: frame at byte %d has version %d, this code reads %d",
				ErrUnsupportedVersion, res.ValidBytes, hdr[2], version)
		}
		if hdr[3] != 0 {
			return corrupt("unknown flags %#x", hdr[3])
		}
		length := binary.LittleEndian.Uint32(hdr[4:8])
		if length > maxPayload { // checked BEFORE using it, so it can never size an allocation
			return corrupt("payload length %d exceeds %d", length, maxPayload)
		}
		seq := domain.Seq(binary.LittleEndian.Uint64(hdr[8:16]))
		if seq != res.LastSeq+1 {
			return corrupt("seq %d after %d", seq, res.LastSeq)
		}

		frame := body[:length+trailerSize]
		if m, err := io.ReadFull(br, frame); err != nil {
			if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
				res.TornBytes = headerSize + int64(m) // header fine, payload cut short
				return res, nil
			}
			return res, fmt.Errorf("journal: reading: %w", err)
		}
		payload := frame[:length]
		if crc32.Checksum(payload, castagnoli) != binary.LittleEndian.Uint32(frame[length:]) {
			return corrupt("payload checksum mismatch")
		}
		c, err := decodeCommand(payload)
		if err != nil {
			return corrupt("%v", err)
		}
		c.Seq = seq

		if fn != nil {
			if err := fn(c); err != nil {
				return res, fmt.Errorf("journal: applying seq %d: %w", seq, err)
			}
		}
		res.Frames++
		res.LastSeq = seq
		res.ValidBytes += headerSize + int64(length) + trailerSize
	}
}
