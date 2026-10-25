package model

import "strings"

// Fractional index keys: base-36 digits, no trailing zero, compared as plain strings.
const digits = "0123456789abcdefghijklmnopqrstuvwxyz"

// ValidKey reports whether k is a legal order key.
func ValidKey(k string) bool {
	if k == "" || len(k) > 256 || k[len(k)-1] == '0' {
		return false
	}
	for i := 0; i < len(k); i++ {
		if strings.IndexByte(digits, k[i]) < 0 {
			return false
		}
	}
	return true
}

// Between returns a key strictly between a and b ("" = open end). Requires a < b when both are set.
func Between(a, b string) string {
	if b != "" && a >= b {
		return a + "i" // defensive: equal/inverted neighbours (concurrent duplicates) append after a
	}
	return midpoint(a, b)
}

func midpoint(a, b string) string {
	if b != "" {
		n := 0
		for n < len(b) {
			ca := byte('0')
			if n < len(a) {
				ca = a[n]
			}
			if ca != b[n] {
				break
			}
			n++
		}
		if n > 0 {
			rest := ""
			if n < len(a) {
				rest = a[n:]
			}
			return b[:n] + midpoint(rest, b[n:])
		}
	}
	da := 0
	if a != "" {
		da = strings.IndexByte(digits, a[0])
	}
	db := len(digits)
	if b != "" {
		db = strings.IndexByte(digits, b[0])
	}
	if db-da > 1 {
		return string(digits[(da+db+1)/2])
	}
	if len(b) > 1 {
		return b[:1]
	}
	rest := ""
	if len(a) > 1 {
		rest = a[1:]
	}
	first := "0"
	if a != "" {
		first = a[:1]
	}
	return first + midpoint(rest, "")
}

// Spread returns n increasing keys of equal width (used for imported documents so they start well spaced).
func Spread(n int) []string {
	w := 1
	for cap := 36; cap < 2*n+2; cap *= 36 {
		w++
	}
	keys := make([]string, n)
	for i := 0; i < n; i++ {
		v := 2*i + 1 // odd => last base-36 digit is never 0
		buf := make([]byte, w)
		for j := w - 1; j >= 0; j-- {
			buf[j] = digits[v%36]
			v /= 36
		}
		keys[i] = string(buf)
	}
	return keys
}

// FillOrders gives every addCell / addPage in ops that has no order key the next key after the current last sibling
// (top of the z-order / last page), counting siblings added earlier in the same batch. It is for API callers (MCP, AI,
// automation) that do not hold the live sibling list; browsers always send explicit keys. ops is modified in place.
func (d *Doc) FillOrders(ops []Op) {
	lastCell := map[string]string{} // page|parent -> last key
	lastPage := ""
	for _, p := range d.SortedPages() {
		lastPage = p.Order.V
	}
	for i := range ops {
		o := &ops[i]
		switch o.T {
		case OpAddPage:
			if o.Order == "" {
				o.Order = Between(lastPage, "")
			}
			lastPage = o.Order
		case OpAddCell:
			if o.Order != "" {
				continue
			}
			k := o.Page + "|" + o.Parent
			last, seen := lastCell[k]
			if !seen {
				if p := d.Pages[o.Page]; p != nil && p.Dead == 0 {
					for _, c := range p.childIndex()[o.Parent] {
						last = c.Order.V
					}
				}
			}
			o.Order = Between(last, "")
			lastCell[k] = o.Order
		}
	}
}
