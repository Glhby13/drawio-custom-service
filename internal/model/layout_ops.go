package model

import (
	"fmt"
	"math"
	"strings"
)

// MaxLayoutCells bounds the work of one hierarchical layout (the cost grows faster than linearly).
const MaxLayoutCells = 1500

// Layout algorithms accepted by LayoutOps besides the original "layered" and "grid".
const (
	AlgoHierarchical   = "hierarchical"    // containers, crossing reduction, routed edges; flows top to bottom
	AlgoHierarchicalLR = "hierarchical-lr" // the same, flowing left to right
)

// ValidAlgo reports whether algo is accepted ("" selects the default, hierarchical).
func ValidAlgo(algo string) bool {
	switch algo {
	case "", "layered", "grid", AlgoHierarchical, AlgoHierarchicalLR:
		return true
	}
	return false
}

// hierarchicalOps re-arranges a whole page with AutoLayout: shapes (also inside containers) get new positions, containers are
// resized to fit, and every edge gets orthogonal routing with ports and waypoints. Only values that actually change produce an
// operation, so running it on an already tidy diagram costs nothing against the AI operation quota.
func (d *Doc) hierarchicalOps(pg *Page, pageID, idPrefix string, horizontal bool) ([]Op, error) {
	var layer string
	for _, c := range pg.Children("0") {
		layer = c.ID
		break
	}
	hasKid := map[string]bool{}
	for _, c := range pg.Cells {
		if c.Alive() && c.Kind == "v" {
			if _, ok := c.Prop("gw"); ok {
				hasKid[c.Parent.V] = true
			}
		}
	}
	if n := len(pg.Cells); n > MaxLayoutCells {
		return nil, layoutErr(fmt.Sprintf("this page has %d cells: hierarchical layout handles up to %d, use algo grid or split the diagram", n, MaxLayoutCells))
	}
	var nodes []LNode
	cells := map[string]*Cell{}
	var walk func(parent, treeParent string)
	walk = func(parent, treeParent string) {
		for _, c := range pg.Children(parent) {
			if c.Kind != "v" {
				continue
			}
			if _, ok := c.Prop("gw"); !ok {
				continue
			}
			st, _ := c.Prop("st:container")
			grp := hasKid[c.ID] || st == "1"
			v, _ := c.Prop("v")
			nodes = append(nodes, LNode{ID: c.ID, Parent: treeParent, W: c.num("gw"), H: c.num("gh"), Group: grp, Title: labelText(v)})
			cells[c.ID] = c
			walk(c.ID, c.ID)
		}
	}
	walk(layer, "")
	if len(nodes) == 0 {
		return nil, nil
	}
	var edges []LEdge
	ecells := map[string]*Cell{}
	for _, c := range sortedCells(pg) {
		if c.Kind != "e" || !c.Alive() {
			continue
		}
		if cells[c.srcID()] == nil || cells[c.tgtID()] == nil {
			continue
		}
		v, _ := c.Prop("v")
		edges = append(edges, LEdge{ID: c.ID, From: c.srcID(), To: c.tgtID(), Labelled: strings.TrimSpace(v) != ""})
		ecells[c.ID] = c
	}
	res := AutoLayout(nodes, edges, LOpts{Horizontal: horizontal})

	var ops []Op
	next := func() string { return fmt.Sprintf("%s-%d", idPrefix, len(ops)) }
	setProp := func(c *Cell, key, val string) {
		if cur, ok := c.Prop(key); ok && cur == val {
			return
		}
		v := val
		ops = append(ops, Op{ID: next(), T: OpSetProp, Page: pageID, Cell: c.ID, Key: key, Val: &v})
	}
	unsetProp := func(c *Cell, key string) {
		if _, ok := c.Prop(key); ok {
			ops = append(ops, Op{ID: next(), T: OpSetProp, Page: pageID, Cell: c.ID, Key: key})
		}
	}
	moved := map[string]bool{}
	for _, n := range nodes { // outer containers first: their children are positioned absolutely afterwards
		c := cells[n.ID]
		p, s := res.Pos[n.ID], res.Size[n.ID]
		if n.Group {
			setProp(c, "gw", fnum(s[0]))
			setProp(c, "gh", fnum(s[1]))
		}
		if cx, cy := pg.Abs(c); moved[n.Parent] || math.Abs(cx-p[0]) > 0.5 || math.Abs(cy-p[1]) > 0.5 {
			moved[n.ID] = true // children keep their offset inside a moved container, so they are re-pinned too
			x, y := Round4(p[0]), Round4(p[1])
			ops = append(ops, Op{ID: next(), T: OpSetPos, Page: pageID, Cell: c.ID, X: &x, Y: &y})
		}
	}
	for _, ed := range edges {
		c, rt := ecells[ed.ID], res.Routes[ed.ID]
		tmp := map[string]string{}
		EdgeStyleProps(tmp, rt, false)
		for _, k := range []string{"st:exitX", "st:exitY", "st:entryX", "st:entryY", "st:exitDx", "st:exitDy", "st:entryDx", "st:entryDy"} {
			if v, ok := tmp[k]; ok {
				setProp(c, k, v)
			}
		}
		if _, ok := c.Prop("st:edgeStyle"); !ok {
			setProp(c, "st:edgeStyle", "orthogonalEdgeStyle")
			setProp(c, "st:rounded", "1")
		}
		if g, ok := tmp["gex"]; ok {
			setProp(c, "gex", g)
		} else {
			unsetProp(c, "gex")
		}
	}
	return ops, nil
}

func sortedCells(pg *Page) []*Cell {
	out := make([]*Cell, 0, len(pg.Cells))
	for _, c := range pg.Cells {
		out = append(out, c)
	}
	sortCells(out)
	return out
}
