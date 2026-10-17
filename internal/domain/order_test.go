package domain

import "testing"

func TestSide(t *testing.T) {
	var unset Side // the zero value: what a forgotten field becomes

	tests := []struct {
		side      Side
		wantValid bool
		wantStr   string
	}{
		{Buy, true, "buy"},
		{Sell, true, "sell"},
		{unset, false, "Side(0)"},
		{Side(7), false, "Side(7)"},
	}
	for _, tc := range tests {
		if got := tc.side.Valid(); got != tc.wantValid {
			t.Errorf("Side(%d).Valid() = %v, want %v", uint8(tc.side), got, tc.wantValid)
		}
		if got := tc.side.String(); got != tc.wantStr {
			t.Errorf("Side(%d).String() = %q, want %q", uint8(tc.side), got, tc.wantStr)
		}
	}
}
