package model

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

var escRepl = strings.NewReplacer(`\`, `\\`, `|`, `\|`, ";", `\;`, "=", `\=`, "\n", `\n`, "\r", `\r`)

func esc(s string) string { return escRepl.Replace(s) }

// Canon is the canonical, order-aware text form of the document state (register VALUES only, no sequence numbers).
// web/model.js produces byte-identical text; both are hashed with SHA-256 (CANON-v1, supersedes the spike's v0).
func (d *Doc) Canon() string {
	var b strings.Builder
	for _, p := range d.SortedPages() {
		b.WriteString("P|" + esc(p.ID) + "|" + esc(p.Name.V) + "|" + p.Order.V + "|" + esc(p.Attrs.V) + "\n")
		idx := p.childIndex()
		var walk func(parent string)
		walk = func(parent string) {
			for _, c := range idx[parent] {
				keys := make([]string, 0, len(c.Props))
				for k, r := range c.Props {
					if !r.Del {
						keys = append(keys, k)
					}
				}
				sort.Strings(keys)
				b.WriteString("C|" + esc(c.ID) + "|" + esc(parent) + "|" + c.Kind + "|" + c.Order.V + "|")
				for _, k := range keys {
					b.WriteString(esc(k) + "=" + esc(c.Props[k].V) + ";")
				}
				b.WriteByte('\n')
				walk(c.ID)
			}
		}
		// the root cell "0" has an empty parent and is excluded from childIndex; emit it first explicitly
		if r := p.Cells["0"]; r != nil && r.Alive() {
			b.WriteString("C|0||" + r.Kind + "|" + r.Order.V + "|\n")
			walk("0")
		}
	}
	return b.String()
}

// Hash is the SHA-256 (hex) of Canon.
func (d *Doc) Hash() string {
	h := sha256.Sum256([]byte(d.Canon()))
	return hex.EncodeToString(h[:])
}
