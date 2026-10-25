package model

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Operation types of the Semantic Operation Contract (ADR-0010).
const (
	OpAddCell     = "addCell"
	OpDeleteCell  = "deleteCell"
	OpSetProp     = "setProp"
	OpSetPos      = "setPos"
	OpConnect     = "connect"
	OpReparent    = "reparent"
	OpReorder     = "reorder"
	OpAddPage     = "addPage"
	OpDeletePage  = "deletePage"
	OpRenamePage  = "renamePage"
	OpReorderPage = "reorderPage"
	OpSetAttrs    = "setPageAttrs"
	OpRestore     = "restoreCells" // revert of deleteCell: resurrects tombstoned cells with their previous state
)

// Actor is stamped by the server from the verified internal token; whatever a client sends is ignored.
type Actor struct {
	ID         string `json:"id"`
	Type       string `json:"type"` // human | ai | service
	Name       string `json:"name,omitempty"`
	OnBehalfOf string `json:"obo,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Model      string `json:"model,omitempty"`
}

// CellSnap is the full state of a deleted cell, kept in the resolved op so a revert can restore it.
type CellSnap struct {
	ID     string            `json:"id"`
	Kind   string            `json:"k"`
	Parent string            `json:"p"`
	Order  string            `json:"o"`
	Props  map[string]string `json:"props"`
}

// Prev holds the values an op replaced (server side, for revert and for the overridden event).
type Prev struct {
	Had    bool       `json:"had,omitempty"`
	Val    string     `json:"val,omitempty"`
	Parent string     `json:"parent,omitempty"`
	Order  string     `json:"order,omitempty"`
	RX     string     `json:"rx,omitempty"`
	RY     string     `json:"ry,omitempty"`
	Name   string     `json:"name,omitempty"`
	Cells  []CellSnap `json:"cells,omitempty"`
}

// ClrEntry names an edge end that lost its terminal when the terminal cell was deleted.
type ClrEntry struct {
	Cell string `json:"cell"`
	End  string `json:"end"`
	Term string `json:"term"`
}

// Resolved is computed once by the sequencer; the log stores it and replay never recomputes it (idempotency).
type Resolved struct {
	RX   string     `json:"rx,omitempty"`
	RY   string     `json:"ry,omitempty"`
	Del  []string   `json:"del,omitempty"` // deleteCell: cell + descendants tombstoned
	Clr  []ClrEntry `json:"clr,omitempty"` // terminals cleared because the terminal died
	Prev *Prev      `json:"prev,omitempty"`
}

// Op is one semantic operation, authored (client/MCP/AI) or resolved+sequenced (server).
type Op struct {
	ID     string            `json:"id"`
	T      string            `json:"t"`
	Page   string            `json:"page,omitempty"`
	Cell   string            `json:"cell,omitempty"`
	Kind   string            `json:"kind,omitempty"`
	Parent string            `json:"parent,omitempty"`
	Order  string            `json:"order,omitempty"`
	Props  map[string]string `json:"props,omitempty"`
	Key    string            `json:"key,omitempty"`
	Val    *string           `json:"val,omitempty"` // nil on setProp = remove the key
	X      *float64          `json:"x,omitempty"`   // setPos: absolute
	Y      *float64          `json:"y,omitempty"`
	End    string            `json:"end,omitempty"` // connect: src | tgt
	Term   string            `json:"term,omitempty"`
	Name   string            `json:"name,omitempty"`
	Attrs  string            `json:"attrs,omitempty"`
	Cells  []CellSnap        `json:"cells,omitempty"` // restoreCells
	// envelope
	Seq   int64     `json:"seq,omitempty"`
	Batch string    `json:"batch,omitempty"`
	Actor *Actor    `json:"actor,omitempty"`
	Res   *Resolved `json:"r,omitempty"`
}

// OpError is a validation/authorization failure with a stable machine code.
type OpError struct {
	Index int    `json:"index"`
	Code  string `json:"code"`
	Msg   string `json:"msg,omitempty"`
}

func (e *OpError) Error() string { return fmt.Sprintf("op %d: %s %s", e.Index, e.Code, e.Msg) }

var (
	idRe     = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,128}$`)
	attrName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\-]{0,63}$`)
	styleKey = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)
)

const (
	maxValueBytes   = 64 * 1024
	maxPropsPerCell = 400
	maxCellsPerPage = 20000
	maxPages        = 100
)

var plainKeys = map[string]bool{"v": true, "vt": true, "sn": true, "gw": true, "gh": true, "grel": true, "gex": true, "fvis": true, "fcol": true, "fcon": true}

// ValidPropKey reports whether k may be written through setProp.
func ValidPropKey(k string) bool {
	switch {
	case plainKeys[k]:
		return true
	case strings.HasPrefix(k, "va:"):
		return attrName.MatchString(k[3:])
	case strings.HasPrefix(k, "st:"):
		return styleKey.MatchString(k[3:])
	}
	return false
}

func validInitialKey(k string) bool {
	return k == "gx" || k == "gy" || k == "src" || k == "tgt" || ValidPropKey(k)
}

// checkValue enforces the value domain of the typed props so that every replica canonicalises identically.
func checkValue(k, v string) string {
	switch k {
	case "gx", "gy", "gw", "gh":
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || Num(f) != v {
			return "bad_number"
		}
	case "grel":
		if v != "1" {
			return "bad_value"
		}
	case "fvis", "fcon":
		if v != "0" {
			return "bad_value"
		}
	case "fcol":
		if v != "1" {
			return "bad_value"
		}
	case "vt":
		if v != "UserObject" && v != "object" {
			return "bad_value"
		}
	case "gex":
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(v), &m) != nil || len(m) == 0 {
			return "bad_value"
		}
		for key := range m {
			if key != "p" && key != "sp" && key != "tp" && key != "off" && key != "alt" {
				return "bad_value"
			}
		}
	}
	return ""
}

func validTerminalKey(end string) bool { return end == "src" || end == "tgt" }

// Validate checks an authored op against the current document; "" means valid.
func (d *Doc) Validate(o *Op) string {
	if o.ID == "" || len(o.ID) > 128 {
		return "bad_op_id"
	}
	pg := d.Pages[o.Page]
	pageOK := pg != nil && pg.Dead == 0
	cell := func(id string) *Cell {
		if !pageOK {
			return nil
		}
		c := pg.Cells[id]
		if c == nil || !c.Alive() {
			return nil
		}
		return c
	}
	switch o.T {
	case OpAddPage:
		if !idRe.MatchString(o.Page) {
			return "bad_page_id"
		}
		if _, ok := d.Pages[o.Page]; ok {
			return "duplicate_page"
		}
		if len(d.SortedPages()) >= maxPages {
			return "too_many_pages"
		}
		if !ValidKey(o.Order) {
			return "bad_order_key"
		}
		if len(o.Name) > 256 || len(o.Attrs) > maxValueBytes {
			return "too_large"
		}
	case OpDeletePage:
		if !pageOK {
			return "page_gone"
		}
		if len(d.SortedPages()) <= 1 {
			return "last_page"
		}
	case OpRenamePage:
		if !pageOK {
			return "page_gone"
		}
		if len(o.Name) > 256 {
			return "too_large"
		}
	case OpReorderPage:
		if !pageOK {
			return "page_gone"
		}
		if !ValidKey(o.Order) {
			return "bad_order_key"
		}
	case OpSetAttrs:
		if !pageOK {
			return "page_gone"
		}
		if len(o.Attrs) > maxValueBytes {
			return "too_large"
		}
	case OpAddCell:
		if !pageOK {
			return "page_gone"
		}
		if !idRe.MatchString(o.Cell) {
			return "bad_cell_id"
		}
		if _, ok := pg.Cells[o.Cell]; ok {
			return "duplicate_cell"
		}
		if len(pg.Cells) >= maxCellsPerPage {
			return "too_many_cells"
		}
		if o.Kind != "v" && o.Kind != "e" && o.Kind != "-" {
			return "bad_kind"
		}
		if o.Parent == "" {
			if o.Cell != "0" {
				return "parent_gone"
			}
			if o.Order != "" && !ValidKey(o.Order) {
				return "bad_order_key"
			}
		} else {
			if o.Cell == "0" || cell(o.Parent) == nil {
				return "parent_gone"
			}
			if cell(o.Parent).Kind == "e" && o.Props["grel"] != "1" {
				return "bad_parent" // only relative-geometry cells (edge labels) may live under an edge
			}
			if !ValidKey(o.Order) {
				return "bad_order_key"
			}
		}
		if len(o.Props) > maxPropsPerCell {
			return "too_large"
		}
		for k, v := range o.Props {
			if !validInitialKey(k) || len(v) > maxValueBytes {
				return "bad_prop"
			}
			if code := checkValue(k, v); code != "" {
				return code
			}
			if (k == "src" || k == "tgt") && (o.Kind != "e" || cell(v) == nil) {
				return "terminal_gone"
			}
			if (k == "src" || k == "tgt") && v == o.Cell {
				return "bad_terminal"
			}
		}
		_, hv := o.Props["v"]
		_, hvt := o.Props["vt"]
		if hv && hvt {
			return "bad_value"
		}
		for k := range o.Props {
			if strings.HasPrefix(k, "va:") && !hvt {
				return "no_xml_value"
			}
		}
		_, hx := o.Props["gx"]
		_, hy := o.Props["gy"]
		_, hw := o.Props["gw"]
		_, hh := o.Props["gh"]
		if (hx || hy || hw || hh) && !(hx && hy && hw && hh) {
			return "partial_geometry"
		}
		if _, hr := o.Props["grel"]; hr && !hw {
			return "partial_geometry"
		}
	case OpRestore:
		if !pageOK {
			return "page_gone"
		}
		if len(o.Cells) == 0 || len(o.Cells) > maxCellsPerPage {
			return "bad_value"
		}
		pending, all := map[string]bool{}, map[string]bool{}
		for _, sc := range o.Cells {
			all[sc.ID] = true
		}
		for _, sc := range o.Cells {
			c := pg.Cells[sc.ID]
			if c == nil || c.Alive() || c.Kind != sc.Kind {
				return "cell_not_deleted"
			}
			pending[sc.ID] = true
			if pa := cell(sc.Parent); pa == nil && !pending[sc.Parent] {
				return "parent_gone"
			}
			if !ValidKey(sc.Order) {
				return "bad_order_key"
			}
			for k, v := range sc.Props {
				if !validInitialKey(k) || len(v) > maxValueBytes {
					return "bad_prop"
				}
				if (k == "src" || k == "tgt") && cell(v) == nil && !all[v] { // terminals may be restored later in the same list
					return "terminal_gone"
				}
			}
		}
	case OpDeleteCell:
		c := cell(o.Cell)
		if c == nil {
			return "cell_gone"
		}
		if o.Cell == "0" {
			return "protected_cell"
		}
		if c.Parent.V == "0" && len(pg.Children("0")) <= 1 {
			return "last_layer"
		}
	case OpSetProp:
		if cell(o.Cell) == nil || o.Cell == "0" {
			return "cell_gone"
		}
		if o.Key == "gx" || o.Key == "gy" { // plain x/y are only writable on relative-geometry cells (edge labels); everything else uses setPos
			if !isRel(cell(o.Cell)) || o.Val == nil {
				return "bad_prop"
			}
		} else if !ValidPropKey(o.Key) {
			return "bad_prop"
		}
		if o.Val != nil && len(*o.Val) > maxValueBytes {
			return "too_large"
		}
		if o.Val != nil {
			if code := checkValue(o.Key, *o.Val); code != "" {
				return code
			}
		}
		if o.Key == "gw" || o.Key == "gh" || o.Key == "grel" || o.Key == "gex" {
			c := cell(o.Cell)
			if _, has := c.Prop("gw"); !has {
				return "no_geometry"
			}
			if o.Val == nil && (o.Key == "gw" || o.Key == "gh") {
				return "bad_value"
			}
		}
		c := cell(o.Cell)
		_, hasVT := c.Prop("vt")
		_, hasV := c.Prop("v")
		switch {
		case o.Key == "vt" && o.Val == nil && c.hasAttrs():
			return "bad_value"
		case o.Key == "vt" && o.Val != nil && hasV:
			return "xml_value" // delete the plain value first
		case o.Key == "v" && o.Val != nil && hasVT:
			return "xml_value"
		case strings.HasPrefix(o.Key, "va:") && o.Val != nil && !hasVT:
			return "no_xml_value"
		}
	case OpSetPos:
		if cell(o.Cell) == nil || o.Cell == "0" {
			return "cell_gone"
		}
		if o.X == nil || o.Y == nil || math.IsNaN(*o.X) || math.IsInf(*o.X, 0) || math.IsNaN(*o.Y) || math.IsInf(*o.Y, 0) {
			return "bad_position"
		}
		c := cell(o.Cell)
		if _, has := c.Prop("gw"); !has {
			return "no_geometry"
		}
		if v, _ := c.Prop("grel"); v == "1" {
			return "relative_geometry"
		}
	case OpConnect:
		c := cell(o.Cell)
		if c == nil || c.Kind != "e" {
			return "cell_gone"
		}
		if !validTerminalKey(o.End) {
			return "bad_prop"
		}
		if o.Term != "" && (cell(o.Term) == nil || o.Term == "0") {
			return "terminal_gone"
		}
		if o.Term == o.Cell {
			return "bad_terminal"
		}
	case OpReparent:
		c := cell(o.Cell)
		if c == nil || o.Cell == "0" {
			return "cell_gone"
		}
		if cell(o.Parent) == nil {
			return "parent_gone"
		}
		if o.Parent == o.Cell || pg.isDescendant(o.Cell, o.Parent) {
			return "cycle"
		}
		if cell(o.Parent).Kind == "e" && !isRel(c) {
			return "bad_parent"
		}
		if !ValidKey(o.Order) {
			return "bad_order_key"
		}
		if c.Parent.V == "0" && o.Parent != "0" && len(pg.Children("0")) <= 1 {
			return "last_layer"
		}
	case OpReorder:
		if cell(o.Cell) == nil || o.Cell == "0" {
			return "cell_gone"
		}
		if !ValidKey(o.Order) {
			return "bad_order_key"
		}
	default:
		return "unknown_op"
	}
	return ""
}

func (p *Page) relTo(parent string, ax, ay float64) (float64, float64) {
	if parent == "" || p.Cells[parent] == nil {
		return ax, ay
	}
	px, py := p.Abs(p.Cells[parent])
	return Round4(ax - px), Round4(ay - py)
}

func snap(c *Cell) CellSnap {
	s := CellSnap{ID: c.ID, Kind: c.Kind, Parent: c.Parent.V, Order: c.Order.V, Props: map[string]string{}}
	for k, r := range c.Props {
		if !r.Del {
			s.Props[k] = r.V
		}
	}
	return s
}

// Resolve computes the sequencer-side form of a validated op (relative geometry, cascade, previous values).
func (d *Doc) Resolve(o *Op) *Op {
	q := *o
	q.Res = &Resolved{Prev: &Prev{}}
	pg := d.Pages[o.Page]
	switch o.T {
	case OpSetProp:
		if r, ok := pg.Cells[o.Cell].Props[o.Key]; ok && !r.Del {
			q.Res.Prev.Had, q.Res.Prev.Val = true, r.V
		}
	case OpSetPos:
		c := pg.Cells[o.Cell]
		rx, ry := pg.relTo(c.Parent.V, *o.X, *o.Y)
		q.Res.RX, q.Res.RY = Num(rx), Num(ry)
		q.Res.Prev.RX, q.Res.Prev.RY = Num(c.num("gx")), Num(c.num("gy"))
	case OpReparent:
		c := pg.Cells[o.Cell]
		if _, has := c.Prop("gw"); has && !isRel(c) { // keep the absolute position (ADR-0010 §2); relative geometry (edge labels) is untouched
			ax, ay := pg.Abs(c)
			rx, ry := pg.relTo(o.Parent, ax, ay)
			q.Res.RX, q.Res.RY = Num(rx), Num(ry)
			q.Res.Prev.RX, q.Res.Prev.RY = Num(c.num("gx")), Num(c.num("gy"))
		}
		q.Res.Prev.Parent, q.Res.Prev.Order = c.Parent.V, c.Order.V
	case OpReorder:
		q.Res.Prev.Order = pg.Cells[o.Cell].Order.V
	case OpConnect:
		key := o.End
		if r, ok := pg.Cells[o.Cell].Props[key]; ok && !r.Del {
			q.Res.Prev.Had, q.Res.Prev.Val = true, r.V
		}
	case OpRenamePage:
		q.Res.Prev.Name = pg.Name.V
	case OpReorderPage:
		q.Res.Prev.Order = pg.Order.V
	case OpDeleteCell:
		dead := map[string]bool{o.Cell: true}
		order := []string{o.Cell}
		for changed := true; changed; {
			changed = false
			ids := make([]string, 0, len(pg.Cells))
			for id := range pg.Cells {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				c := pg.Cells[id]
				if c.Alive() && !dead[id] && c.Parent.V != "" && dead[c.Parent.V] {
					dead[id], changed = true, true
					order = append(order, id)
				}
			}
		}
		q.Res.Del = order
		for _, id := range order {
			q.Res.Prev.Cells = append(q.Res.Prev.Cells, snap(pg.Cells[id]))
		}
		ids := make([]string, 0, len(pg.Cells))
		for id := range pg.Cells {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			c := pg.Cells[id]
			if c.Alive() && !dead[id] && c.Kind == "e" {
				for _, t := range []string{"src", "tgt"} {
					if r, ok := c.Props[t]; ok && !r.Del && dead[r.V] {
						q.Res.Clr = append(q.Res.Clr, ClrEntry{Cell: id, End: t, Term: r.V})
					}
				}
			}
		}
	}
	return &q
}

func isRel(c *Cell) bool { v, _ := c.Prop("grel"); return v == "1" }

func (c *Cell) hasAttrs() bool {
	for k, r := range c.Props {
		if strings.HasPrefix(k, "va:") && !r.Del {
			return true
		}
	}
	return false
}

func (c *Cell) setProp(k, v string, seq int64) {
	r := c.Props[k]
	setReg(&r, v, seq)
	c.Props[k] = r
}

func (c *Cell) delProp(k string, seq int64) {
	r := c.Props[k]
	delReg(&r, seq)
	c.Props[k] = r
}

// Apply applies a RESOLVED op; it is a pure function of (state, op) and idempotent for the same op and seq.
func (d *Doc) Apply(o *Op) {
	s := o.Seq
	r := o.Res
	if r == nil {
		r = &Resolved{}
	}
	pg := d.Pages[o.Page]
	switch o.T {
	case OpAddPage:
		if p, ok := d.Pages[o.Page]; ok && p.CreatedBy == o.ID {
			return
		}
		d.Pages[o.Page] = &Page{ID: o.Page, CreatedBy: o.ID, Name: Reg{V: o.Name, Seq: s}, Order: Reg{V: o.Order, Seq: s}, Attrs: Reg{V: o.Attrs, Seq: s}, Cells: map[string]*Cell{}}
	case OpDeletePage:
		if pg != nil && pg.Dead == 0 {
			pg.Dead = s
		}
	case OpRenamePage:
		setReg(&pg.Name, o.Name, s)
	case OpReorderPage:
		setReg(&pg.Order, o.Order, s)
	case OpSetAttrs:
		setReg(&pg.Attrs, o.Attrs, s)
	case OpAddCell:
		if c, ok := pg.Cells[o.Cell]; ok && c.CreatedBy == o.ID {
			return
		}
		c := &Cell{ID: o.Cell, Kind: o.Kind, CreatedBy: o.ID, Parent: Reg{V: o.Parent, Seq: s}, Order: Reg{V: o.Order, Seq: s}, Props: map[string]Reg{}}
		for k, v := range o.Props {
			c.Props[k] = Reg{V: v, Seq: s}
		}
		pg.Cells[o.Cell] = c
	case OpSetProp:
		if o.Val == nil {
			pg.Cells[o.Cell].delProp(o.Key, s)
		} else {
			pg.Cells[o.Cell].setProp(o.Key, *o.Val, s)
		}
	case OpSetPos:
		c := pg.Cells[o.Cell]
		c.setProp("gx", r.RX, s)
		c.setProp("gy", r.RY, s)
	case OpConnect:
		c := pg.Cells[o.Cell]
		if o.Term == "" {
			c.delProp(o.End, s)
		} else {
			c.setProp(o.End, o.Term, s)
		}
	case OpReorder:
		setReg(&pg.Cells[o.Cell].Order, o.Order, s)
	case OpReparent:
		c := pg.Cells[o.Cell]
		setReg(&c.Parent, o.Parent, s)
		setReg(&c.Order, o.Order, s)
		if r.RX != "" {
			c.setProp("gx", r.RX, s)
			c.setProp("gy", r.RY, s)
		}
	case OpRestore:
		for _, sc := range o.Cells {
			c := pg.Cells[sc.ID]
			c.Dead = 0
			c.Parent = Reg{V: sc.Parent, Seq: s}
			c.Order = Reg{V: sc.Order, Seq: s}
			c.Props = map[string]Reg{}
			for k, v := range sc.Props {
				c.Props[k] = Reg{V: v, Seq: s}
			}
		}
	case OpDeleteCell:
		for _, id := range r.Del {
			if c := pg.Cells[id]; c != nil && c.Dead == 0 {
				c.Dead = s
			}
		}
		for _, e := range r.Clr {
			if c := pg.Cells[e.Cell]; c != nil {
				c.delProp(e.End, s)
			}
		}
	}
}

// ApplyBestEffort applies each op on its own (used by revert): ops that no longer apply are skipped, not fatal.
func (d *Doc) ApplyBestEffort(ops []Op) (applied []Op, skipped []OpError) {
	for i := range ops {
		out, oe := d.ApplyBatch(ops[i : i+1])
		if oe != nil {
			oe.Index = i
			skipped = append(skipped, *oe)
			continue
		}
		applied = append(applied, out...)
	}
	return
}

// ApplyBatch validates, resolves and applies ops atomically. On failure the document is unchanged and the
// failing index is returned. Sequence numbers are d.Seq+1.. in order.
func (d *Doc) ApplyBatch(ops []Op) ([]Op, *OpError) {
	work := d
	if len(ops) > 1 {
		work = d.Clone()
	}
	out := make([]Op, 0, len(ops))
	for i := range ops {
		o := ops[i]
		o.Res, o.Seq = nil, 0
		if code := work.Validate(&o); code != "" {
			return nil, &OpError{Index: i, Code: code}
		}
		res := work.Resolve(&o)
		res.Seq = work.Seq + 1
		work.Apply(res)
		work.Seq = res.Seq
		out = append(out, *res)
	}
	if work != d {
		*d = *work
	}
	return out, nil
}
