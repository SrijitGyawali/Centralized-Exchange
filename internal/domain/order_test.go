package domain

import "testing"

func TestSide(t *testing.T) {
	var unset Side // the zero value: what a forgotten field becomes

	tests := []struct {
		side         Side
		wantValid    bool
		wantStr      string
		wantOpposite Side
	}{
		{Buy, true, "buy", Sell},
		{Sell, true, "sell", Buy},
		{unset, false, "Side(0)", unset},
		{Side(7), false, "Side(7)", Side(7)},
	}
	for _, tc := range tests {
		if got := tc.side.Valid(); got != tc.wantValid {
			t.Errorf("Side(%d).Valid() = %v, want %v", uint8(tc.side), got, tc.wantValid)
		}
		if got := tc.side.String(); got != tc.wantStr {
			t.Errorf("Side(%d).String() = %q, want %q", uint8(tc.side), got, tc.wantStr)
		}
		if got := tc.side.Opposite(); got != tc.wantOpposite {
			t.Errorf("Side(%d).Opposite() = %v, want %v", uint8(tc.side), got, tc.wantOpposite)
		}
	}
}

func TestTimeInForce(t *testing.T) {
	var unset TimeInForce

	tests := []struct {
		tif       TimeInForce
		wantValid bool
		wantStr   string
	}{
		{GTC, true, "gtc"},
		{IOC, true, "ioc"},
		{unset, false, "TimeInForce(0)"},
		{TimeInForce(9), false, "TimeInForce(9)"},
	}
	for _, tc := range tests {
		if got := tc.tif.Valid(); got != tc.wantValid {
			t.Errorf("TimeInForce(%d).Valid() = %v, want %v", uint8(tc.tif), got, tc.wantValid)
		}
		if got := tc.tif.String(); got != tc.wantStr {
			t.Errorf("TimeInForce(%d).String() = %q, want %q", uint8(tc.tif), got, tc.wantStr)
		}
	}
}
