package model

import "fmt"

// InverseOps builds authored ops that undo a batch of RESOLVED ops (given in sequence order). They are returned in the
// order they must be applied (reverse). Ops that cannot be inverted (page deletion, page attrs) are reported as skipped.
// The current document is only used to turn previous relative positions into absolute targets.
func InverseOps(cur *Doc, batch []Op, idPrefix string) (inv []Op, skipped []string) {
	addedPages := map[string]bool{}
	for _, o := range batch {
		if o.T == OpAddPage {
			addedPages[o.Page] = true
		}
	}
	n := 0
	id := func() string { n++; return fmt.Sprintf("%s-%d", idPrefix, n) }
	for i := len(batch) - 1; i >= 0; i-- {
		o := batch[i]
		r := o.Res
		if r == nil || r.Prev == nil {
			r = &Resolved{Prev: &Prev{}}
		}
		switch o.T {
		case OpAddCell:
			if addedPages[o.Page] {
				continue // the whole page goes away with its addPage inverse
			}
			pg := cur.Pages[o.Page]
			if pg != nil {
				if c := pg.Cells[o.Cell]; c != nil && !c.Alive() {
					// Cell added in this batch was later deleted; re-create/restore it at its batch state.
					props := o.Props
					if len(props) == 0 {
						props = snap(c).Props
					}
					parent, order := o.Parent, o.Order
					if parent == "" {
						parent = c.Parent.V
					}
					if order == "" {
						order = c.Order.V
					}
					inv = append(inv, Op{ID: id(), T: OpRestore, Page: o.Page, Cells: []CellSnap{{
						ID: o.Cell, Kind: o.Kind, Parent: parent, Order: order, Props: props,
					}}})
					continue
				}
			}
			inv = append(inv, Op{ID: id(), T: OpDeleteCell, Page: o.Page, Cell: o.Cell})
		case OpDeleteCell:
			pg := cur.Pages[o.Page]
			if pg != nil {
				if c := pg.Cells[o.Cell]; c != nil && c.Alive() {
					inv = append(inv, Op{ID: id(), T: OpDeleteCell, Page: o.Page, Cell: o.Cell})
					continue
				}
			}
			if len(r.Prev.Cells) > 0 {
				inv = append(inv, Op{ID: id(), T: OpRestore, Page: o.Page, Cells: r.Prev.Cells})
			}
			for _, e := range r.Clr {
				inv = append(inv, Op{ID: id(), T: OpConnect, Page: o.Page, Cell: e.Cell, End: e.End, Term: e.Term})
			}
		case OpRestore:
			pg := cur.Pages[o.Page]
			var toRestore []CellSnap
			for _, sc := range o.Cells {
				if pg != nil {
					if c := pg.Cells[sc.ID]; c != nil && !c.Alive() {
						toRestore = append(toRestore, sc)
						continue
					}
				}
				inv = append(inv, Op{ID: id(), T: OpDeleteCell, Page: o.Page, Cell: sc.ID})
			}
			if len(toRestore) > 0 {
				inv = append(inv, Op{ID: id(), T: OpRestore, Page: o.Page, Cells: toRestore})
			}
		case OpSetProp:
			pg := cur.Pages[o.Page]
			if pg != nil {
				if c := pg.Cells[o.Cell]; c != nil && !c.Alive() {
					inv = append(inv, Op{ID: id(), T: OpRestore, Page: o.Page, Cells: []CellSnap{snap(c)}})
				}
			}
			op := Op{ID: id(), T: OpSetProp, Page: o.Page, Cell: o.Cell, Key: o.Key}
			if r.Prev.Had {
				v := r.Prev.Val
				if pg != nil && pg.Cells[o.Cell] != nil {
					if curV, ok := pg.Cells[o.Cell].Prop(o.Key); ok && curV == v && o.Val != nil {
						v = *o.Val
					}
				}
				op.Val = &v
			}
			inv = append(inv, op)
		case OpSetPos:
			pg := cur.Pages[o.Page]
			if pg == nil || pg.Cells[o.Cell] == nil {
				skipped = append(skipped, o.T+" "+o.Cell)
				continue
			}
			c := pg.Cells[o.Cell]
			if !c.Alive() {
				inv = append(inv, Op{ID: id(), T: OpRestore, Page: o.Page, Cells: []CellSnap{snap(c)}})
			}
			px, py := 0.0, 0.0
			if p := pg.Cells[c.Parent.V]; p != nil {
				px, py = pg.Abs(p)
			}
			targetRX, targetRY := r.Prev.RX, r.Prev.RY
			if c.Alive() && Num(c.num("gx")) == r.Prev.RX && Num(c.num("gy")) == r.Prev.RY && r.RX != "" {
				targetRX, targetRY = r.RX, r.RY
			}
			x, y := Round4(px+parseNum(targetRX)), Round4(py+parseNum(targetRY))
			inv = append(inv, Op{ID: id(), T: OpSetPos, Page: o.Page, Cell: o.Cell, X: &x, Y: &y})
		case OpReparent:
			inv = append(inv, Op{ID: id(), T: OpReparent, Page: o.Page, Cell: o.Cell, Parent: r.Prev.Parent, Order: r.Prev.Order})
		case OpReorder:
			inv = append(inv, Op{ID: id(), T: OpReorder, Page: o.Page, Cell: o.Cell, Order: r.Prev.Order})
		case OpConnect:
			inv = append(inv, Op{ID: id(), T: OpConnect, Page: o.Page, Cell: o.Cell, End: o.End, Term: r.Prev.Val})
		case OpRenamePage:
			inv = append(inv, Op{ID: id(), T: OpRenamePage, Page: o.Page, Name: r.Prev.Name})
		case OpReorderPage:
			inv = append(inv, Op{ID: id(), T: OpReorderPage, Page: o.Page, Order: r.Prev.Order})
		case OpAddPage:
			inv = append(inv, Op{ID: id(), T: OpDeletePage, Page: o.Page})
		default:
			skipped = append(skipped, o.T+" "+o.Cell)
		}
	}
	return
}
