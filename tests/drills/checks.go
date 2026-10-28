package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// audit is what an offline, read-only replay of a journal finds: the money
// checks. It uses the same journal.Scan and state.Apply as the server and
// cmd/replay, and never modifies the file (a torn tail is only measured).
type audit struct {
	Frames      int    `json:"frames"`
	LastSeq     uint64 `json:"last_seq"`
	ValidBytes  int64  `json:"valid_bytes"`
	TornBytes   int64  `json:"torn_bytes"`
	Invariants  string `json:"invariants"` // "ok" or the violation
	Digest      string `json:"digest"`
	IssuedUSDT  int64  `json:"issued_usdt_atoms"`
	IssuedBTC   int64  `json:"issued_btc_lots"`
	SumUSDT     int64  `json:"sum_user_usdt_atoms"` // available + reserved over every drill user
	SumBTC      int64  `json:"sum_user_btc_lots"`
	Conserved   bool   `json:"conserved"` // every unit issued is held by exactly one user bucket
	stopAtSeq   uint64
	finalDigest [32]byte
}

var errStop = errors.New("stop")

// auditJournal replays path up to toSeq (0 = everything).
func auditJournal(path string, toSeq uint64) (audit, error) {
	f, err := os.Open(path) // read-only
	if err != nil {
		return audit{}, err
	}
	defer f.Close()
	st := state.New()
	res, err := journal.Scan(f, func(c command.Command) error {
		if toSeq != 0 && uint64(c.Seq) > toSeq {
			return errStop
		}
		_, err := st.Apply(c)
		return err
	})
	if err != nil && !errors.Is(err, errStop) {
		return audit{}, fmt.Errorf("replaying %s: %w", path, err)
	}
	a := audit{Frames: res.Frames, LastSeq: uint64(st.LastSeq()), ValidBytes: res.ValidBytes, TornBytes: res.TornBytes,
		Invariants: "ok", IssuedUSDT: st.Issued(domain.USDT), IssuedBTC: st.Issued(domain.BTC)}
	if err := st.CheckInvariants(); err != nil {
		a.Invariants = err.Error()
	}
	a.finalDigest = st.Digest()
	a.Digest = fmt.Sprintf("%x", a.finalDigest)
	// Conservation, checked independently of CheckInvariants: every unit the
	// system ever issued must sit in exactly one user's available or
	// reserved bucket. (The drill's users are the only ones ever funded.)
	for i := range users {
		for _, asset := range []domain.Asset{domain.USDT, domain.BTC} {
			avail, reserved := st.Balance(domain.UserID(userID(i)), asset)
			if asset == domain.USDT {
				a.SumUSDT += avail + reserved
			} else {
				a.SumBTC += avail + reserved
			}
		}
	}
	a.Conserved = a.SumUSDT == a.IssuedUSDT && a.SumBTC == a.IssuedBTC
	return a, nil
}

// isPrefix reports whether the first n bytes of copyPath equal the first n
// bytes of origPath: a restored journal must be an exact prefix of the
// original, never a different history.
func isPrefix(copyPath, origPath string, n int64) (bool, error) {
	a, err := readN(copyPath, n)
	if err != nil {
		return false, err
	}
	b, err := readN(origPath, n)
	if err != nil {
		return false, err
	}
	return bytes.Equal(a, b), nil
}

func readN(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	_, err = io.ReadFull(f, buf)
	return buf, err
}

// copyFile copies src to dst while src may still be growing: a "hot" copy,
// which is what a naive file-level backup of a running server gets.
func copyFile(dst, src string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}
