// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-002 §fuzz).
// Document model + deterministic apply rules of the HYPOTHESIS in ADR-0006:
// semantic operations, LWW registers per property (ordered by the server sequence), add-wins insert, tombstone delete
// with cascade, fractional-index ordering, server-validated move (no cycles), abs-position-preserving reparent.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Mutations switch deliberate bugs on, to prove the fuzzer can detect them.
var Mut = struct {
	NoCycleCheck bool // reparent without cycle validation
	NoCascade    bool // delete does not tombstone descendants
	UnstableSort bool // sibling order without id tiebreak, map iteration order leaks
	DropAckSome  bool // server "forgets" to confirm some ops to the sender
	SilentDrop   bool // server loses some ops without ack/nack (accounting property must catch it)
	NoAuthz      bool // server skips permission checks (authz property must catch it)
}{}

type Reg struct {
	V   string
	Seq int64
}

type Cell struct {
	ID, Page  string
	Kind      byte // 'v' vertex, 'e' edge
	CreatedBy string
	Parent    Reg // V = parent cell id, "" = page root
	Order     Reg // fractional index among siblings
	Props     map[string]Reg
	Dead      int64
}

type Page struct {
	ID        string
	Name      Reg
	Order     Reg
	Dead      int64
	CreatedBy string
}

type Doc struct {
	Pages map[string]*Page
	Cells map[string]*Cell // cell ids are unique per document in the model
}

func NewDoc() *Doc { return &Doc{Pages: map[string]*Page{}, Cells: map[string]*Cell{}} }

func (d *Doc) Clone() *Doc {
	n := NewDoc()
	for k, p := range d.Pages {
		c := *p
		n.Pages[k] = &c
	}
	for k, c := range d.Cells {
		cc := *c
		cc.Props = make(map[string]Reg, len(c.Props))
		for pk, pv := range c.Props {
			cc.Props[pk] = pv
		}
		n.Cells[k] = &cc
	}
	return n
}

// Op is the semantic operation envelope (ADR-0010 candidate, reduced to what the model needs).
type Op struct {
	ID     string
	Actor  int
	CSeq   int
	Base   int64
	Seq    int64 // assigned by the server
	T      string
	Page   string
	Cell   string
	Kind   byte
	Par    string // parent (addCell, reparent)
	Key    string // order key (addCell/reparent/reorder/addPage/reorderPage) or prop key (setProp)
	Val    string
	X, Y   int  // abs target (INTENT) or relative (NAIVE)
	Rel    bool // true = NAIVE geometry (X,Y relative to parent as seen by the author)
	Name   string
	IX, IY int // ORACLE ONLY: absolute position the author intended (never read by apply/validate)
	// Filled by the SEQUENCER (Resolve); the log stores resolved ops, replay never recomputes them.
	Resolved bool
	RX, RY   int      // parent-relative geometry
	RDel     []string // cells tombstoned by deleteCell (cell + descendants at sequencing time)
	RClr     []string // "edge:src|tgt" terminals cleared because the terminal died
	Term     string   // connect: "src"|"tgt"
	TermV    string
}

// ---- helpers -----------------------------------------------------------------------------------------------------

func (c *Cell) alive() bool { return c.Dead == 0 }

func (d *Doc) prop(c *Cell, k string) int {
	if r, ok := c.Props[k]; ok {
		n, _ := strconv.Atoi(r.V)
		return n
	}
	return 0
}

func (d *Doc) Abs(c *Cell) (int, int) {
	x, y := d.prop(c, "gx"), d.prop(c, "gy")
	seen := 0
	for p := c.Parent.V; p != ""; {
		pc, ok := d.Cells[p]
		if !ok || seen > 1000 {
			break
		}
		x += d.prop(pc, "gx")
		y += d.prop(pc, "gy")
		p = pc.Parent.V
		seen++
	}
	return x, y
}

func (d *Doc) isDescendant(anc, c string) bool {
	for p, n := d.Cells[c].Parent.V, 0; p != "" && n < 10000; n++ {
		if p == anc {
			return true
		}
		pc, ok := d.Cells[p]
		if !ok {
			return false
		}
		p = pc.Parent.V
	}
	return false
}

func setReg(r *Reg, v string, seq int64) {
	if seq >= r.Seq { // LWW; >= makes re-application of the same op idempotent
		r.V, r.Seq = v, seq
	}
}

// ---- validation (server side; pure function of the current doc) ------------------------------------------------

func (d *Doc) Validate(o *Op) string {
	pageOK := func(id string) bool { p, ok := d.Pages[id]; return ok && p.Dead == 0 }
	cellOK := func(id string) bool {
		c, ok := d.Cells[id]
		return ok && c.alive() && d.Pages[c.Page].Dead == 0
	}
	switch o.T {
	case "addPage":
		if _, ok := d.Pages[o.Page]; ok {
			return "duplicate_page"
		}
		if !ValidKey(o.Key) {
			return "bad_order_key"
		}
	case "deletePage", "renamePage", "reorderPage":
		if !pageOK(o.Page) {
			return "page_gone"
		}
		if o.T == "reorderPage" && !ValidKey(o.Key) {
			return "bad_order_key"
		}
	case "addCell":
		if !pageOK(o.Page) {
			return "page_gone"
		}
		if _, ok := d.Cells[o.Cell]; ok {
			return "duplicate_cell"
		}
		if o.Par != "" && (!cellOK(o.Par) || d.Cells[o.Par].Page != o.Page) {
			return "parent_gone"
		}
		if !ValidKey(o.Key) {
			return "bad_order_key"
		}
	case "deleteCell", "setProp", "setPos", "reorder":
		if !cellOK(o.Cell) {
			return "cell_gone"
		}
		if o.T == "reorder" && !ValidKey(o.Key) {
			return "bad_order_key"
		}
	case "connect":
		if !cellOK(o.Cell) || d.Cells[o.Cell].Kind != 'e' {
			return "cell_gone"
		}
		if o.TermV != "" && (!cellOK(o.TermV) || d.Cells[o.TermV].Page != d.Cells[o.Cell].Page) {
			return "terminal_gone"
		}
	case "reparent":
		if !cellOK(o.Cell) {
			return "cell_gone"
		}
		if o.Par != "" {
			if !cellOK(o.Par) || d.Cells[o.Par].Page != d.Cells[o.Cell].Page {
				return "parent_gone"
			}
			if !Mut.NoCycleCheck && (o.Par == o.Cell || d.isDescendant(o.Cell, o.Par)) {
				return "cycle"
			}
		}
		if !ValidKey(o.Key) {
			return "bad_order_key"
		}
	default:
		return "unknown_op"
	}
	return ""
}

// ---- resolve (sequencer only): turns the authored intent into a self-contained, replay-safe operation ---------------

func (d *Doc) relTo(parent string, ax, ay int) (int, int) {
	if parent == "" {
		return ax, ay
	}
	px, py := d.Abs(d.Cells[parent])
	return ax - px, ay - py
}

func (d *Doc) Resolve(o *Op) *Op {
	q := *o
	q.Resolved = true
	switch o.T {
	case "addCell":
		if o.Rel {
			q.RX, q.RY = o.X, o.Y
		} else {
			q.RX, q.RY = d.relTo(o.Par, o.X, o.Y)
		}
	case "setPos":
		if o.Rel {
			q.RX, q.RY = o.X, o.Y
		} else {
			q.RX, q.RY = d.relTo(d.Cells[o.Cell].Parent.V, o.X, o.Y)
		}
	case "reparent":
		if o.Rel {
			q.RX, q.RY = o.X, o.Y
		} else { // INTENT: keep the absolute position the cell has at sequencing time
			ax, ay := d.Abs(d.Cells[o.Cell])
			q.RX, q.RY = d.relTo(o.Par, ax, ay)
		}
	case "deleteCell":
		dead := map[string]bool{o.Cell: true}
		q.RDel = []string{o.Cell}
		if !Mut.NoCascade {
			for changed := true; changed; {
				changed = false
				ids := make([]string, 0, len(d.Cells))
				for id := range d.Cells {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					cc := d.Cells[id]
					if cc.Dead == 0 && !dead[id] && cc.Parent.V != "" && dead[cc.Parent.V] {
						dead[id], changed = true, true
						q.RDel = append(q.RDel, id)
					}
				}
			}
		}
		ids := make([]string, 0, len(d.Cells))
		for id := range d.Cells {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			cc := d.Cells[id]
			if cc.Dead == 0 && !dead[id] && cc.Kind == 'e' {
				for _, t := range []string{"src", "tgt"} {
					if r, ok := cc.Props[t]; ok && r.V != "" && dead[r.V] {
						q.RClr = append(q.RClr, id+":"+t)
					}
				}
			}
		}
	}
	return &q
}

// ---- apply (pure function of the resolved op; idempotent) -------------------------------------------------------------

func (d *Doc) setProp(c *Cell, k, v string, s int64) {
	r := c.Props[k]
	setReg(&r, v, s)
	c.Props[k] = r
}

func (d *Doc) Apply(o *Op) {
	if !o.Resolved { // optimistic (client-side) view: resolve against the local state
		o = d.Resolve(o)
	}
	s := o.Seq
	switch o.T {
	case "addPage":
		if p, ok := d.Pages[o.Page]; ok && p.CreatedBy == o.ID {
			return
		}
		d.Pages[o.Page] = &Page{ID: o.Page, CreatedBy: o.ID, Name: Reg{o.Name, s}, Order: Reg{o.Key, s}}
	case "deletePage":
		if p := d.Pages[o.Page]; p != nil && p.Dead == 0 {
			p.Dead = s
			for _, c := range d.Cells {
				if c.Page == o.Page && c.Dead == 0 {
					c.Dead = s
				}
			}
		}
	case "renamePage":
		setReg(&d.Pages[o.Page].Name, o.Name, s)
	case "reorderPage":
		setReg(&d.Pages[o.Page].Order, o.Key, s)
	case "addCell":
		if c, ok := d.Cells[o.Cell]; ok && c.CreatedBy == o.ID {
			return
		}
		c := &Cell{ID: o.Cell, Page: o.Page, Kind: o.Kind, CreatedBy: o.ID, Parent: Reg{o.Par, s}, Order: Reg{o.Key, s}, Props: map[string]Reg{}}
		d.Cells[o.Cell] = c
		c.Props["gx"], c.Props["gy"] = Reg{strconv.Itoa(o.RX), s}, Reg{strconv.Itoa(o.RY), s}
		c.Props["value"] = Reg{o.Val, s}
	case "setProp":
		d.setProp(d.Cells[o.Cell], o.Key, o.Val, s)
	case "setPos":
		c := d.Cells[o.Cell]
		d.setProp(c, "gx", strconv.Itoa(o.RX), s)
		d.setProp(c, "gy", strconv.Itoa(o.RY), s)
	case "connect":
		d.setProp(d.Cells[o.Cell], o.Term, o.TermV, s)
	case "reorder":
		setReg(&d.Cells[o.Cell].Order, o.Key, s)
	case "reparent":
		c := d.Cells[o.Cell]
		setReg(&c.Parent, o.Par, s)
		setReg(&c.Order, o.Key, s)
		d.setProp(c, "gx", strconv.Itoa(o.RX), s)
		d.setProp(c, "gy", strconv.Itoa(o.RY), s)
	case "deleteCell":
		for _, id := range o.RDel {
			if c := d.Cells[id]; c != nil && c.Dead == 0 {
				c.Dead = s
			}
		}
		for _, e := range o.RClr {
			i := strings.LastIndexByte(e, ':')
			if c := d.Cells[e[:i]]; c != nil {
				d.setProp(c, e[i+1:], "", s)
			}
		}
	}
}

// ---- canonical form / checksum -----------------------------------------------------------------------------------

func sortedKeys(m map[string]Reg) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func (d *Doc) children(page, parent string) []*Cell {
	var out []*Cell
	for _, c := range d.Cells { // map iteration order is random on purpose
		if c.Page == page && c.alive() && c.Parent.V == parent {
			out = append(out, c)
		}
	}
	if Mut.UnstableSort {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Order.V < out[j].Order.V })
	} else {
		sort.Slice(out, func(i, j int) bool {
			if out[i].Order.V != out[j].Order.V {
				return out[i].Order.V < out[j].Order.V
			}
			return out[i].ID < out[j].ID
		})
	}
	return out
}

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

func (d *Doc) Canon() string {
	var b strings.Builder
	var walk func(page, parent string, depth int)
	walk = func(page, parent string, depth int) {
		for _, c := range d.children(page, parent) {
			fmt.Fprintf(&b, "C|%s|%s|%c|%s|", c.ID, parent, c.Kind, c.Order.V)
			for _, k := range sortedKeys(c.Props) { // register VALUES only: seq numbers are not part of the state
				fmt.Fprintf(&b, "%s=%s;", k, c.Props[k].V)
			}
			b.WriteByte('\n')
			walk(page, c.ID, depth+1)
		}
	}
	for _, p := range d.SortedPages() {
		fmt.Fprintf(&b, "P|%s|%s|%s\n", p.ID, p.Name.V, p.Order.V)
		walk(p.ID, "", 0)
	}
	return b.String()
}

func (d *Doc) Hash() string {
	h := sha256.Sum256([]byte(d.Canon()))
	return hex.EncodeToString(h[:])
}

// ---- fractional index (base-36 digits, no trailing zero) -----------------------------------------------------------

const digits = "0123456789abcdefghijklmnopqrstuvwxyz"

func ValidKey(k string) bool {
	if k == "" || k[len(k)-1] == '0' {
		return false
	}
	for i := 0; i < len(k); i++ {
		if strings.IndexByte(digits, k[i]) < 0 {
			return false
		}
	}
	return true
}

// Between returns a key strictly between a and b ("" = open end). a < b required when both are non-empty.
func Between(a, b string) string {
	if b != "" && a >= b {
		panic("Between: a >= b: " + a + " " + b)
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
