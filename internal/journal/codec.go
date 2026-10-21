package journal

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// The payload encoding of one command (format version 1). The frame around
// it (frame.go) carries the Seq, so the payload holds everything else:
//
//	kind      1 byte    1 = Fund, 2 = PlaceOrder, 3 = CancelOrder
//	user      uvarint
//	key len   uvarint   0..command.MaxKeyLen
//	key       bytes
//	then, per kind:
//	  Fund:        to uvarint | asset 1 byte | amount varint
//	  PlaceOrder:  side 1 byte | tif 1 byte | price varint | qty varint
//	  CancelOrder: order uvarint
//
// Varints (encoding/binary) store small numbers in few bytes: a uvarint uses
// 7 bits per byte, and a signed varint first "zigzags" -1, 1, -2, 2 … onto
// 1, 2, 3, 4 … so small negatives stay small too.
//
// The encoding is CANONICAL: every command has exactly one byte string. The
// decoder rejects anything else (padded varints, trailing bytes), and
// FuzzDecodeCommand checks that decode-then-encode returns the input. Field
// values are recorded exactly as sent, even invalid ones (side 7, a negative
// amount): the state rejects those, and replay must reproduce the same
// rejection.

const (
	kindFund   byte = 1
	kindPlace  byte = 2
	kindCancel byte = 3
)

var errBadPayload = errors.New("bad payload")

// appendCommand appends c's canonical encoding (without Seq) to dst.
func appendCommand(dst []byte, c command.Command) ([]byte, error) {
	if len(c.Key) > command.MaxKeyLen {
		return dst, fmt.Errorf("journal: key of %d bytes exceeds %d", len(c.Key), command.MaxKeyLen)
	}
	var kind byte
	switch c.Body.(type) {
	case command.Fund:
		kind = kindFund
	case command.PlaceOrder:
		kind = kindPlace
	case command.CancelOrder:
		kind = kindCancel
	default:
		return dst, fmt.Errorf("journal: cannot encode body %T", c.Body)
	}

	dst = append(dst, kind)
	dst = binary.AppendUvarint(dst, uint64(c.User))
	dst = binary.AppendUvarint(dst, uint64(len(c.Key)))
	dst = append(dst, c.Key...)
	switch b := c.Body.(type) {
	case command.Fund:
		dst = binary.AppendUvarint(dst, uint64(b.To))
		dst = append(dst, byte(b.Asset))
		dst = binary.AppendVarint(dst, b.Amount)
	case command.PlaceOrder:
		dst = append(dst, byte(b.Side), byte(b.TIF))
		dst = binary.AppendVarint(dst, int64(b.Price))
		dst = binary.AppendVarint(dst, int64(b.Qty))
	case command.CancelOrder:
		dst = binary.AppendUvarint(dst, uint64(b.Order))
	}
	return dst, nil
}

// decodeCommand parses one payload. Every malformed input is an error, never
// a panic and never an oversized allocation: lengths are checked against
// both the remaining bytes and MaxKeyLen before anything is copied.
func decodeCommand(b []byte) (command.Command, error) {
	d := decoder{b: b}
	kind := d.byte()
	c := command.Command{User: domain.UserID(d.uvarint())}
	if n := d.uvarint(); n > command.MaxKeyLen {
		d.fail("key length %d exceeds %d", n, command.MaxKeyLen)
	} else {
		c.Key = string(d.bytes(int(n)))
	}
	switch kind {
	case kindFund:
		c.Body = command.Fund{To: domain.UserID(d.uvarint()), Asset: domain.Asset(d.byte()), Amount: d.varint()}
	case kindPlace:
		c.Body = command.PlaceOrder{
			Side: domain.Side(d.byte()), TIF: domain.TimeInForce(d.byte()),
			Price: domain.Price(d.varint()), Qty: domain.Qty(d.varint()),
		}
	case kindCancel:
		c.Body = command.CancelOrder{Order: domain.OrderID(d.uvarint())}
	default:
		d.fail("unknown body kind %d", kind)
	}
	if d.err == nil && len(d.b) > 0 {
		d.fail("%d trailing bytes", len(d.b))
	}
	if d.err != nil {
		return command.Command{}, d.err
	}
	return c, nil
}

// decoder reads fields front to back. After the first error every read
// returns zero, so callers check d.err once at the end instead of after
// every field. This is the same pattern as bufio.Scanner and encoding/binary.
type decoder struct {
	b   []byte
	err error
}

func (d *decoder) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: %s", errBadPayload, fmt.Sprintf(format, args...))
	}
}

func (d *decoder) byte() byte {
	if d.err != nil || len(d.b) < 1 {
		d.fail("truncated")
		return 0
	}
	v := d.b[0]
	d.b = d.b[1:]
	return v
}

func (d *decoder) bytes(n int) []byte {
	if d.err != nil || n > len(d.b) {
		d.fail("truncated")
		return nil
	}
	v := d.b[:n]
	d.b = d.b[n:]
	return v
}

func (d *decoder) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.b)
	if n <= 0 { // 0: not enough bytes; < 0: more than 64 bits
		d.fail("bad uvarint")
		return 0
	}
	var buf [binary.MaxVarintLen64]byte
	if binary.PutUvarint(buf[:], v) != n { // e.g. 1 written as 0x81 0x00
		d.fail("non-canonical uvarint")
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *decoder) varint() int64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Varint(d.b)
	if n <= 0 {
		d.fail("bad varint")
		return 0
	}
	var buf [binary.MaxVarintLen64]byte
	if binary.PutVarint(buf[:], v) != n {
		d.fail("non-canonical varint")
		return 0
	}
	d.b = d.b[n:]
	return v
}
