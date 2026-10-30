package model

import "testing"

// Appending many siblings (an AI building a large diagram through the API) must keep keys valid and ordered for thousands of
// cells; Between itself is a browser contract and is left alone (see TestDiffOpsFromBrowserAreAcceptedByGo).
func TestFillOrdersKeysStayValidForLargePages(t *testing.T) {
	last := ""
	for i := 0; i < 8000; i++ {
		k := appendKey(last)
		if !ValidKey(k) || (last != "" && k <= last) {
			t.Fatalf("i=%d last=%q key=%q valid=%v", i, last, k, ValidKey(k))
		}
		last = k
	}
	// a browser can still insert between two generated keys
	a, b := appendKey("zz"), appendKey(appendKey("zz"))
	if m := Between(a, b); !ValidKey(m) || m <= a || m >= b {
		t.Fatalf("Between(%q,%q)=%q", a, b, m)
	}
}
