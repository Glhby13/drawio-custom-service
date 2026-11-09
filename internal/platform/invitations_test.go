package platform

import (
	"testing"
	"time"
)

func TestExpiryOfInvites(t *testing.T) {
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	iptr := func(i int) *int { return &i }

	if x, ok := inviteExpiry(nil, now); !ok || x == nil || !x.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("default: %v %v", x, ok)
	}
	if x, ok := inviteExpiry(iptr(0), now); !ok || x != nil {
		t.Fatalf("0 = never: %v %v", x, ok)
	}
	if x, ok := inviteExpiry(iptr(1), now); !ok || x == nil || !x.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("1: %v %v", x, ok)
	}
	if x, ok := inviteExpiry(iptr(365), now); !ok || x == nil || !x.Equal(now.Add(365*24*time.Hour)) {
		t.Fatalf("365: %v %v", x, ok)
	}
	for _, d := range []int{366, -1, 1000} {
		if x, ok := inviteExpiry(iptr(d), now); ok || x != nil {
			t.Fatalf("%d must be invalid: %v %v", d, x, ok)
		}
	}
}

func TestWIBDayBounds(t *testing.T) {
	// 16:59Z = 23:59 WIB: still the same WIB day; 17:00Z = 00:00 WIB: the next one
	before := time.Date(2026, 11, 9, 16, 59, 0, 0, time.UTC)
	after := time.Date(2026, 11, 9, 17, 0, 0, 0, time.UTC)
	wantStart := time.Date(2026, 11, 8, 17, 0, 0, 0, time.UTC) // 00:00 WIB on 9 Nov
	if got := startOfDayWIB(before); !got.Equal(wantStart) {
		t.Fatalf("start before: %v want %v", got, wantStart)
	}
	if got := startOfDayWIB(after); !got.Equal(after) {
		t.Fatalf("start after: %v want %v", got, after)
	}
	if l := startOfDayWIB(before).In(wib); l.Day() != 9 || l.Hour() != 0 {
		t.Fatalf("not 2026-11-09 00:00 WIB: %v", l)
	}
	if l := startOfDayWIB(after).In(wib); l.Day() != 10 || l.Hour() != 0 {
		t.Fatalf("not 2026-11-10 00:00 WIB: %v", l)
	}
	if got := nextMidnightWIB(before); !got.Equal(after) {
		t.Fatalf("next midnight of 16:59Z: %v want %v", got, after)
	}
	for _, ts := range []time.Time{before, after, time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC), time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)} {
		if d := nextMidnightWIB(ts).Sub(startOfDayWIB(ts)); d != 24*time.Hour {
			t.Fatalf("%v: a WIB day lasts %v", ts, d)
		}
		if !nextMidnightWIB(ts).After(ts) || startOfDayWIB(ts).After(ts) {
			t.Fatalf("%v: day bounds do not contain the instant", ts)
		}
	}
}

func TestNewAndValidInviteToken(t *testing.T) {
	tok, hash, err := newInviteToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 47 || tok[:4] != InvitePrefix {
		t.Fatalf("token %q", tok)
	}
	if !validInviteToken(tok) {
		t.Fatalf("a fresh token is not valid: %q", tok)
	}
	if hash != hashToken(tok) || len(hash) != 64 || hash == tok {
		t.Fatalf("hash %q", hash)
	}
	tok2, _, _ := newInviteToken()
	if tok2 == tok {
		t.Fatal("tokens repeat")
	}
	for _, bad := range []string{"", "dsi_", tok[:46], tok + "A", "xxx_" + tok[4:], tok[4:], "dsm_" + tok[4:]} {
		if validInviteToken(bad) {
			t.Fatalf("%q must be invalid", bad)
		}
	}
}
