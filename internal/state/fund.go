package state

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
)

// fund creates test money: the system's issuance account goes down by
// Amount, and the recipient's available balance goes up by the same.
//
//	Funding journal:  issuance  -amount
//	                  user      +amount     (sums to zero)
//
// Total issuance may never exceed the asset's MaxSupply. That cap is what
// guarantees no balance can ever overflow (see domain.Asset.MaxSupply).
func (s *State) fund(t *tx, f command.Fund) command.Result {
	switch {
	case f.To == SystemUser:
		return reject(command.RejectInvalid, "fund: recipient user must be non-zero")
	case !f.Asset.Valid():
		return reject(command.RejectInvalid, "fund: unknown asset %v", f.Asset)
	case f.Amount <= 0:
		return reject(command.RejectInvalid, "fund: amount must be positive, got %d", f.Amount)
	case f.Amount > f.Asset.MaxSupply()-s.Issued(f.Asset): // written to avoid overflow
		return reject(command.RejectSupplyCap, "fund: %s %v would exceed the max supply of %s (issued so far %s)",
			f.Asset.Format(f.Amount), f.Asset, f.Asset.Format(f.Asset.MaxSupply()), f.Asset.Format(s.Issued(f.Asset)))
	}

	t.post(Funding, 0, 0,
		Posting{Account{SystemUser, Issuance}, f.Asset, -f.Amount},
		Posting{Account{f.To, Available}, f.Asset, f.Amount},
	)
	return command.Result{}
}

// reject builds a rejected Result with a human-readable detail.
func reject(reason command.RejectReason, format string, args ...any) command.Result {
	return command.Result{Rejected: reason, Detail: fmt.Sprintf(format, args...)}
}
