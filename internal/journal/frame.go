package journal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
)

// One frame on disk (format version 1). All integers are little-endian.
//
//	offset  size  field
//	0       2     magic "CX"
//	2       1     format version (1)
//	3       1     flags (must be 0; reserved)
//	4       4     payload length (≤ maxPayload)
//	8       8     Seq
//	16      4     header CRC-32C of bytes 0..15
//	20      n     payload (codec.go)
//	20+n    4     payload CRC-32C of the payload
//
// Why TWO checksums? Recovery must tell apart "a crash cut our last write
// short" (a torn tail, safe to drop) from "a complete frame was damaged"
// (corruption, never skip). With its own checksum, a header can be
// trusted before the payload is even read, so a damaged LENGTH can never
// make a complete frame look like a short, torn one.
//
// CRC-32C (Castagnoli) detects accidental damage such as bit flips and
// torn sectors, and is hardware-accelerated on modern CPUs. It is NOT a
// security measure: anyone able to edit the file can recompute it.

const (
	headerSize  = 20
	trailerSize = 4
	maxPayload  = 256 // generous: the largest real payload is under 120 bytes
	version     = 1
)

var (
	magic      = [2]byte{'C', 'X'}
	castagnoli = crc32.MakeTable(crc32.Castagnoli)
)

// appendFrame appends c, framed and checksummed, to dst.
func appendFrame(dst []byte, c command.Command) ([]byte, error) {
	start := len(dst)
	dst = append(dst, magic[0], magic[1], version, 0)
	dst = binary.LittleEndian.AppendUint32(dst, 0) // length, filled in below
	dst = binary.LittleEndian.AppendUint64(dst, uint64(c.Seq))
	dst = binary.LittleEndian.AppendUint32(dst, 0) // header CRC, filled in below

	payloadStart := len(dst)
	dst, err := appendCommand(dst, c)
	if err != nil {
		return dst[:start], err
	}
	n := len(dst) - payloadStart
	if n > maxPayload {
		return dst[:start], fmt.Errorf("journal: payload of %d bytes exceeds %d", n, maxPayload)
	}

	binary.LittleEndian.PutUint32(dst[start+4:], uint32(n))
	binary.LittleEndian.PutUint32(dst[start+16:], crc32.Checksum(dst[start:start+16], castagnoli))
	return binary.LittleEndian.AppendUint32(dst, crc32.Checksum(dst[payloadStart:], castagnoli)), nil
}
