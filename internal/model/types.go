// Package model is the single source of truth for a draw.io document on the server side: pages, cells, per-key
// LWW registers, the Semantic Operation contract (ADR-0010) with its authored and RESOLVED forms, validation,
// deterministic apply, canonical checksum (CANON, mirrored by web/model.js), draw.io XML import/export, revert,
// layout and SVG export. It has no I/O and is architecture-neutral (ADR-0011).
package model

import (
	"math"
	"sort"
	"strconv"
)

// Reg is a last-writer-wins register ordered by the server sequence.
type Reg struct {
	V   string `json:"v"`
	Seq int64  `json:"s"`
	Del bool   `json:"d,omitempty"` // key removed (tombstone)
}

// Cell is one mxCell. Props are namespaced registers (see keys.go).
type Cell struct {
	ID        string         `json:"id"`
	Kind      string         `json:"k"` // "v" vertex, "e" edge, "-" root/layer
	Parent    Reg            `json:"p"`
	Order     Reg            `json:"o"`
	Props     map[string]Reg `json:"props"`
	Dead      int64          `json:"dead,omitempty"`
	CreatedBy string         `json:"cb,omitempty"`
}

// Page is one diagram page; cell ids are unique per page (draw.io guarantee), not per document.
type Page struct {
	ID        string           `json:"id"`
	Name      Reg              `json:"name"`
	Order     Reg              `json:"order"`
	Attrs     Reg              `json:"attrs"` // mxGraphModel attributes as a JSON object, replaced as a unit
	Dead      int64            `json:"dead,omitempty"`
	CreatedBy string           `json:"cb,omitempty"`
	Cells     map[string]*Cell `json:"cells"`
}

// Doc is the document state at Seq.
type Doc struct {
	Pages map[string]*Page `json:"pages"`
	Seq   int64            `json:"seq"`
}

// NewDoc returns an empty document.
func NewDoc() *Doc { return &Doc{Pages: map[string]*Page{}} }

// Clone deep-copies the document.
func (d *Doc) Clone() *Doc {
	n := &Doc{Pages: make(map[string]*Page, len(d.Pages)), Seq: d.Seq}
	for id, p := range d.Pages {
		cp := *p
		cp.Cells = make(map[string]*Cell, len(p.Cells))
		for cid, c := range p.Cells {
			cc := *c
			cc.Props = make(map[string]Reg, len(c.Props))
			for k, v := range c.Props {
				cc.Props[k] = v
			}
			cp.Cells[cid] = &cc
		}
		n.Pages[id] = &cp
	}
	return n
}

// Alive reports whether the cell is not tombstoned.
func (c *Cell) Alive() bool { return c.Dead == 0 }

// Prop returns the value of a prop and whether it is set.
func (c *Cell) Prop(k string) (string, bool) {
	r, ok := c.Props[k]
	if !ok || r.Del {
		return "", false
	}
	return r.V, true
}

// Round4 rounds half up to 4 decimals exactly like the browser model does (floor(x*1e4+0.5)/1e4).
func Round4(x float64) float64 { return math.Floor(x*1e4+0.5) / 1e4 }

// Num is the canonical number text: shortest round-trip decimal, never an exponent, "-0" folded to "0".
func Num(f float64) string {
	if f == 0 {
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func parseNum(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func (c *Cell) num(k string) float64 {
	v, _ := c.Prop(k)
	return parseNum(v)
}

// Abs returns the absolute position of a cell (sum of x/y along the parent chain).
func (p *Page) Abs(c *Cell) (float64, float64) {
	x, y := c.num("gx"), c.num("gy")
	for q, n := c.Parent.V, 0; q != "" && n < 10000; n++ {
		pc, ok := p.Cells[q]
		if !ok {
			break
		}
		x += pc.num("gx")
		y += pc.num("gy")
		q = pc.Parent.V
	}
	return Round4(x), Round4(y)
}

func (p *Page) isDescendant(anc, c string) bool {
	for q, n := p.Cells[c].Parent.V, 0; q != "" && n < 10000; n++ {
		if q == anc {
			return true
		}
		pc, ok := p.Cells[q]
		if !ok {
			return false
		}
		q = pc.Parent.V
	}
	return false
}

// Children returns the alive children of parent in sibling order (order key, then id).
func (p *Page) Children(parent string) []*Cell {
	var out []*Cell
	for _, c := range p.Cells {
		if c.Alive() && c.Parent.V == parent && c.ID != "0" {
			out = append(out, c)
		}
	}
	sortCells(out)
	return out
}

func sortCells(cs []*Cell) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Order.V != cs[j].Order.V {
			return cs[i].Order.V < cs[j].Order.V
		}
		return cs[i].ID < cs[j].ID
	})
}

// SortedPages returns the alive pages in order.
func (d *Doc) SortedPages() []*Page {
	var ps []*Page
	for _, p := range d.Pages {
		if p.Dead == 0 {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Order.V != ps[j].Order.V {
			return ps[i].Order.V < ps[j].Order.V
		}
		return ps[i].ID < ps[j].ID
	})
	return ps
}

// childIndex groups alive cells by parent once (O(N)) for exports.
func (p *Page) childIndex() map[string][]*Cell {
	idx := map[string][]*Cell{}
	for _, c := range p.Cells {
		if c.Alive() && c.ID != "0" {
			idx[c.Parent.V] = append(idx[c.Parent.V], c)
		}
	}
	for k := range idx {
		sortCells(idx[k])
	}
	return idx
}

func setReg(r *Reg, v string, seq int64) {
	if seq >= r.Seq { // LWW; >= makes re-application of the very same op idempotent
		r.V, r.Seq, r.Del = v, seq, false
	}
}

func delReg(r *Reg, seq int64) {
	if seq >= r.Seq {
		r.V, r.Seq, r.Del = "", seq, true
	}
}
