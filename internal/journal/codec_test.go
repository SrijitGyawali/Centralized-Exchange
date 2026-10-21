package journal

import (
	"bytes"
	"encoding/hex"
	"math"
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// sampleCommands covers every body kind and the extremes of every field.
// Commands the state will REJECT must round-trip exactly too: replay has to
// reproduce the same rejection, so the journal records requests as they
// were sent, not as they "should" have been.
func sampleCommands() []command.Command {
	return []command.Command{
		{User: 99, Key: "fund-1", Body: command.Fund{To: 7, Asset: domain.USDT, Amount: 10_000 * domain.AtomsPerUSDT}},
		{User: 7, Key: "k", Body: command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1_000, TIF: domain.GTC}},
		{User: 7, Key: strings.Repeat("x", command.MaxKeyLen), Body: command.CancelOrder{Order: 42}},
		{User: 0, Key: "", Body: command.Fund{To: 0, Asset: 0, Amount: -5}},
		{User: math.MaxUint64, Key: "ünïcödé ✓", Body: command.PlaceOrder{Side: 255, Price: math.MinInt64, Qty: math.MaxInt64, TIF: 9}},
		{User: 3, Key: "z", Body: command.CancelOrder{Order: math.MaxUint64}},
	}
}

func TestCodecRoundTrip(t *testing.T) {
	for _, c := range sampleCommands() {
		b, err := appendCommand(nil, c)
		if err != nil {
			t.Fatalf("appendCommand(%#v): %v", c, err)
		}
		got, err := decodeCommand(b)
		if err != nil {
			t.Fatalf("decodeCommand(%x): %v", b, err)
		}
		if got != c { // Command is comparable: strings, ints and a comparable Body
			t.Errorf("round trip changed the command:\n got  %#v\n want %#v", got, c)
		}
	}
}

// TestGoldenEncoding pins the on-disk payload format byte for byte. The
// expected hex below was worked out by hand from the format description in
// codec.go. If this test fails, the file format changed: every existing
// journal would become unreadable unless the format VERSION is bumped and
// the old one is still decoded. Change it only deliberately.
func TestGoldenEncoding(t *testing.T) {
	tests := []struct {
		name string
		cmd  command.Command
		hex  string
	}{
		{
			// kind 01 | user 01 | key len 02 "ab" | to 07 | asset 02 | amount zigzag(1,000,000)=2,000,000 → 80 89 7a
			"fund", command.Command{User: 1, Key: "ab", Body: command.Fund{To: 7, Asset: domain.USDT, Amount: 1_000_000}},
			"0101026162070280897a",
		},
		{
			// kind 02 | user 07 | key len 01 "k" | side 01 | tif 01 | price zigzag(60,000) → c0 a9 07 | qty zigzag(1,000) → d0 0f
			"place", command.Command{User: 7, Key: "k", Body: command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1_000, TIF: domain.GTC}},
			"0207016b0101c0a907d00f",
		},
		{
			// kind 03 | user 03 | key len 01 "c" | order 2a
			"cancel", command.Command{User: 3, Key: "c", Body: command.CancelOrder{Order: 42}},
			"030301632a",
		},
	}
	for _, tc := range tests {
		b, err := appendCommand(nil, tc.cmd)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := hex.EncodeToString(b); got != tc.hex {
			t.Errorf("%s encodes to %s, want %s: the on-disk format changed", tc.name, got, tc.hex)
		}
	}
}

func TestEncodeRejects(t *testing.T) {
	for name, c := range map[string]command.Command{
		"no body":      {User: 1, Key: "k"},
		"key too long": {User: 1, Key: strings.Repeat("x", command.MaxKeyLen+1), Body: command.CancelOrder{Order: 1}},
	} {
		if _, err := appendCommand(nil, c); err == nil {
			t.Errorf("%s: encoded without error", name)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	valid, _ := appendCommand(nil, command.Command{User: 3, Key: "c", Body: command.CancelOrder{Order: 42}})
	tests := map[string][]byte{
		"empty":                {},
		"unknown kind":         append([]byte{0x09}, valid[1:]...),
		"trailing byte":        append(bytes.Clone(valid), 0x00),
		"truncated":            valid[:len(valid)-1],
		"key longer than max":  append([]byte{0x03, 0x01, command.MaxKeyLen + 1}, bytes.Repeat([]byte("x"), command.MaxKeyLen+2)...),
		"key beyond the data":  {0x03, 0x01, 0x0a, 'a', 'b'},
		"non-canonical varint": {0x03, 0x81, 0x00, 0x01, 'c', 0x2a}, // user 1 written as two bytes
		"varint overflow":      append([]byte{0x03}, bytes.Repeat([]byte{0xff}, 11)...),
	}
	for name, b := range tests {
		if c, err := decodeCommand(b); err == nil {
			t.Errorf("%s: decoded %x into %#v, want an error", name, b, c)
		}
	}
}

// FuzzDecodeCommand: any bytes at all must never panic, and whatever does
// decode must re-encode to EXACTLY the same bytes. That proves the encoding
// is canonical: one command, one byte string.
func FuzzDecodeCommand(f *testing.F) {
	for _, c := range sampleCommands() {
		b, _ := appendCommand(nil, c)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := decodeCommand(b)
		if err != nil {
			return
		}
		again, err := appendCommand(nil, c)
		if err != nil {
			t.Fatalf("decoded %#v but cannot re-encode it: %v", c, err)
		}
		if !bytes.Equal(again, b) {
			t.Fatalf("not canonical: %x decodes to %#v, which re-encodes to %x", b, c, again)
		}
	})
}
