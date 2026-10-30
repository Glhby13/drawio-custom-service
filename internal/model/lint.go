package model

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Lint is the feedback loop that a model lacks when it draws blind: it reads the diagram back the way a person would look at it
// and names what is wrong (overlaps, labels that do not fit, edges that run through shapes, edge crossings) together with a fix.
// It is read-only and deterministic. Geometry is an estimate: edge routes are rebuilt from ports and waypoints as draw.io's
// orthogonal edge style would draw them, and text size comes from TextWidth/TextBlock.

// LintIssue is one finding.
type LintIssue struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"` // error | warn | info
	Cells    []string `json:"cells"`
	Msg      string   `json:"message"`
	Fix      string   `json:"fix,omitempty"`
}

// LintReport is the result for one page.
type LintReport struct {
	Page      string      `json:"page"`
	Shapes    int         `json:"shapes"`
	Edges     int         `json:"edges"`
	Crossings int         `json:"crossings"`
	Issues    []LintIssue `json:"issues"`
}

// Clean reports whether nothing at warn level or above was found.
func (r *LintReport) Clean() bool {
	for _, i := range r.Issues {
		if i.Severity != "info" {
			return false
		}
	}
	return true
}

type rect struct{ x, y, w, h float64 }

func (r rect) inset(d float64) rect { return rect{r.x + d, r.y + d, r.w - 2*d, r.h - 2*d} }
func (r rect) overlap(o rect) float64 {
	w := math.Min(r.x+r.w, o.x+o.w) - math.Max(r.x, o.x)
	h := math.Min(r.y+r.h, o.y+o.h) - math.Max(r.y, o.y)
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}
func (r rect) contains(o rect, tol float64) bool {
	return o.x >= r.x-tol && o.y >= r.y-tol && o.x+o.w <= r.x+r.w+tol && o.y+o.h <= r.y+r.h+tol
}

type seg struct{ ax, ay, bx, by float64 }

// hitsRect: axis-aligned segment against a rectangle.
func (s seg) hitsRect(r rect) bool {
	x0, x1 := math.Min(s.ax, s.bx), math.Max(s.ax, s.bx)
	y0, y1 := math.Min(s.ay, s.by), math.Max(s.ay, s.by)
	return x1 > r.x && x0 < r.x+r.w && y1 > r.y && y0 < r.y+r.h
}

// crosses: a proper crossing of two axis-aligned segments (one horizontal, one vertical).
func (s seg) crosses(o seg) bool {
	sh, oh := s.ay == s.by, o.ay == o.by
	if sh == oh {
		return false
	}
	h, v := s, o
	if !sh {
		h, v = o, s
	}
	hx0, hx1 := math.Min(h.ax, h.bx), math.Max(h.ax, h.bx)
	vy0, vy1 := math.Min(v.ay, v.by), math.Max(v.ay, v.by)
	return v.ax > hx0+1 && v.ax < hx1-1 && h.ay > vy0+1 && h.ay < vy1-1
}

// The pairwise checks (overlap, edges through shapes, crossings) are quadratic. Past these sizes they are skipped, with an explicit
// note, so a huge diagram cannot be used to burn CPU through the read-only lint endpoint.
const (
	lintMaxShapes = 1200
	lintMaxEdges  = 1200
)

type lintShape struct {
	c         *Cell
	box       rect
	container bool
	parent    string
}

// Lint inspects one page.
func (d *Doc) Lint(pageID string) (*LintReport, error) {
	pg := d.Pages[pageID]
	if pg == nil || pg.Dead != 0 {
		return nil, errPage
	}
	rep := &LintReport{Page: pageID, Issues: []LintIssue{}}
	shapes := map[string]*lintShape{}
	var order []string
	hasKid := map[string]bool{}
	for _, c := range pg.Cells {
		if c.Alive() && c.Kind == "v" {
			if _, ok := c.Prop("gw"); ok {
				if _, isV := pg.Cells[c.Parent.V]; isV && pg.Cells[c.Parent.V].Kind == "v" {
					hasKid[c.Parent.V] = true
				}
			}
		}
	}
	for _, c := range pg.Cells {
		if !c.Alive() || c.Kind != "v" {
			continue
		}
		if _, ok := c.Prop("gw"); !ok {
			continue
		}
		x, y := pg.Abs(c)
		sh := &lintShape{c: c, box: rect{x, y, c.num("gw"), c.num("gh")}, container: hasKid[c.ID]}
		if p := pg.Cells[c.Parent.V]; p != nil && p.Kind == "v" {
			sh.parent = p.ID
		}
		if st, _ := c.Prop("st:container"); st == "1" {
			sh.container = true
		}
		shapes[c.ID] = sh
		order = append(order, c.ID)
	}
	sort.Strings(order)
	rep.Shapes = len(shapes)

	add := func(code, sev, msg, fix string, cells ...string) {
		rep.Issues = append(rep.Issues, LintIssue{Code: code, Severity: sev, Cells: cells, Msg: msg, Fix: fix})
	}
	label := func(c *Cell) string { v, _ := c.Prop("v"); return strings.TrimSpace(labelText(v)) }
	name := func(id string) string {
		if sh := shapes[id]; sh != nil {
			if l := label(sh.c); l != "" {
				return fmt.Sprintf("%s (%q)", id, trunc(l, 28))
			}
		}
		return id
	}

	nEdges := 0
	for _, c := range pg.Cells {
		if c.Alive() && c.Kind == "e" {
			nEdges++
		}
	}
	heavy := len(order) <= lintMaxShapes && nEdges <= lintMaxEdges
	if !heavy {
		add("lint_partial", "info", fmt.Sprintf("the diagram is large (%d shapes, over the limit of %d shapes / %d edges): overlap, edge-through-shape and crossing checks were skipped", len(order), lintMaxShapes, lintMaxEdges),
			"lint a smaller page, or split the diagram")
	}
	// 1. siblings that overlap, children that stick out of their container
	bySib := map[string][]string{}
	for _, id := range order {
		bySib[shapes[id].parent] = append(bySib[shapes[id].parent], id)
	}
	for _, sib := range sortedKeys(bySib) {
		ids := bySib[sib]
		if !heavy {
			break
		}
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				a, b := shapes[ids[i]], shapes[ids[j]]
				if ov := a.box.inset(1).overlap(b.box.inset(1)); ov > 16 {
					add("overlap", "error", fmt.Sprintf("%s and %s overlap (%.0f px²)", name(a.c.ID), name(b.c.ID), ov),
						"run layout with algo=hierarchical, or move one of them", a.c.ID, b.c.ID)
				}
			}
		}
	}
	for _, id := range order {
		sh := shapes[id]
		if sh.parent != "" {
			if p := shapes[sh.parent]; p != nil && !p.box.contains(sh.box, 2) {
				add("outside_container", "warn", fmt.Sprintf("%s sticks out of its container %s", name(id), name(p.c.ID)),
					"enlarge the container or run layout with algo=hierarchical (it sizes containers to fit)", id, p.c.ID)
			}
		}
	}

	// 2. labels that do not fit their shape
	for _, id := range order {
		sh := shapes[id]
		l := label(sh.c)
		if l == "" || sh.container {
			continue
		}
		fs := 12.0
		if v, ok := sh.c.Prop("st:fontSize"); ok {
			if f := parseNum(v); f >= 6 && f <= 72 {
				fs = f
			}
		}
		kf, hf := 1.0, 0.0
		switch shapeName(sh.c) {
		case "rhombus":
			kf = 0.55
		case "ellipse", "cloud":
			kf = 0.68
		case "hexagon":
			kf = 0.8
		case "cylinder3", "cylinder":
			hf = 24
		}
		availW, availH := (sh.box.w-16)*kf, (sh.box.h-10)*kf-hf
		wrap, _ := sh.c.Prop("st:whiteSpace")
		var tw, th float64
		if wrap == "wrap" {
			tw, th, _ = TextBlock(l, availW, fs, false)
			if tw > availW+1 { // a single word wider than the shape
				add("label_overflow", "warn", fmt.Sprintf("the word in %s is wider than the shape (%.0f > %.0f px)", name(id), tw, availW), "widen the shape or shorten the label", id)
				continue
			}
		} else {
			ls := strings.Split(l, "\n")
			for _, x := range ls {
				tw = math.Max(tw, TextWidth(x, fs, false))
			}
			th = float64(len(ls)) * fs * 1.3
		}
		if (wrap != "wrap" && tw > availW+1) || th > availH+2 {
			add("label_overflow", "warn", fmt.Sprintf("the label of %s does not fit (needs about %.0f×%.0f px, has %.0f×%.0f)", name(id), tw, th, availW, availH),
				"make the shape larger (width/height) or shorten the label", id)
		}
	}

	// 3. edges: routes, shapes in the way, label on a shape, crossings
	type routed struct {
		id       string
		src, tgt string
		segs     []seg
	}
	var routes []routed
	ids := make([]string, 0)
	for id, c := range pg.Cells {
		if c.Alive() && c.Kind == "e" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	rep.Edges = len(ids)
	descendantOf := func(id, anc string) bool {
		for q, n := shapes[id], 0; q != nil && q.parent != "" && n < 64; n++ {
			if q.parent == anc {
				return true
			}
			q = shapes[q.parent]
		}
		return false
	}
	for _, id := range ids {
		c := pg.Cells[id]
		src, tgt := shapes[c.srcID()], shapes[c.tgtID()]
		if src == nil || tgt == nil {
			add("dangling_edge", "error", fmt.Sprintf("edge %s is not connected to a shape at both ends", id), "connect both ends or delete the edge", id)
			continue
		}
		if !heavy {
			continue
		}
		pts := edgePolyline(c, src, tgt)
		var segs []seg
		for i := 1; i < len(pts); i++ {
			segs = append(segs, seg{pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1]})
		}
		routes = append(routes, routed{id, src.c.ID, tgt.c.ID, segs})
		hit := 0
		for _, oid := range order {
			o := shapes[oid]
			if o.container || oid == src.c.ID || oid == tgt.c.ID || descendantOf(oid, src.c.ID) || descendantOf(oid, tgt.c.ID) {
				continue
			}
			for _, s := range segs {
				if s.hitsRect(o.box.inset(3)) {
					add("edge_through_shape", "warn", fmt.Sprintf("edge %s (%s → %s) runs through %s", id, name(src.c.ID), name(tgt.c.ID), name(oid)),
						"run layout with algo=hierarchical to re-route edges around shapes, or move the shape", id, oid)
					hit++
					break
				}
			}
			if hit >= 3 {
				break
			}
		}
		if l := label(c); l != "" && len(segs) > 0 {
			mx, my := polyMid(segs)
			for _, oid := range order {
				o := shapes[oid]
				if o.container || oid == src.c.ID || oid == tgt.c.ID {
					continue
				}
				if (rect{mx - 2, my - 2, 4, 4}).overlap(o.box) > 0 {
					add("edge_label_on_shape", "warn", fmt.Sprintf("the label %q of edge %s sits on top of %s", trunc(l, 24), id, name(oid)), "move the shape or run layout with algo=hierarchical", id, oid)
					break
				}
			}
		}
	}
	type pairX struct{ a, b string }
	var xs []pairX
	for i := 0; i < len(routes); i++ {
		for j := i + 1; j < len(routes); j++ {
			a, b := routes[i], routes[j]
			if a.src == b.src || a.src == b.tgt || a.tgt == b.src || a.tgt == b.tgt {
				continue // edges that share an end legitimately meet there
			}
			n := 0
			for _, s := range a.segs {
				for _, t := range b.segs {
					if s.crosses(t) {
						n++
					}
				}
			}
			if n > 0 {
				rep.Crossings += n
				xs = append(xs, pairX{a.id, b.id})
			}
		}
	}
	if rep.Crossings > 0 {
		sev := "info"
		if rep.Crossings > 2 && rep.Crossings*3 > len(routes) {
			sev = "warn"
		}
		cells := []string{}
		for i, p := range xs {
			if i >= 6 {
				break
			}
			cells = append(cells, p.a, p.b)
		}
		add("edge_crossings", sev, fmt.Sprintf("%d edge crossings between %d edge pairs", rep.Crossings, len(xs)),
			"run layout with algo=hierarchical (orders shapes to reduce crossings), or group related shapes in a container", cells...)
	}
	sort.SliceStable(rep.Issues, func(i, j int) bool { return sevRank(rep.Issues[i].Severity) < sevRank(rep.Issues[j].Severity) })
	return rep, nil
}

func sevRank(s string) int {
	switch s {
	case "error":
		return 0
	case "warn":
		return 1
	}
	return 2
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// shapeName is the draw.io shape of a cell: shape=<x>, or a bare style name such as "rhombus" / "ellipse".
func shapeName(c *Cell) string {
	if v, ok := c.Prop("st:shape"); ok {
		return v
	}
	sn, _ := c.Prop("sn")
	for _, t := range strings.Split(sn, ";") {
		switch t {
		case "rhombus", "ellipse":
			return t
		}
	}
	return ""
}

func polyMid(segs []seg) (float64, float64) {
	total := 0.0
	for _, s := range segs {
		total += math.Abs(s.bx-s.ax) + math.Abs(s.by-s.ay)
	}
	half := total / 2
	for _, s := range segs {
		l := math.Abs(s.bx-s.ax) + math.Abs(s.by-s.ay)
		if half <= l && l > 0 {
			t := half / l
			return s.ax + (s.bx-s.ax)*t, s.ay + (s.by-s.ay)*t
		}
		half -= l
	}
	last := segs[len(segs)-1]
	return last.bx, last.by
}

// edgePolyline rebuilds the path draw.io's orthogonal style would take: leave the source on its port, pass the waypoints, enter
// the target on its port. Every segment is horizontal or vertical.
func edgePolyline(c *Cell, src, tgt *lintShape) [][2]float64 {
	var wps [][2]float64
	if raw, ok := c.Prop("gex"); ok {
		var g struct {
			P [][2]float64 `json:"p"`
		}
		if json.Unmarshal([]byte(raw), &g) == nil {
			wps = g.P
		}
	}
	tc := [2]float64{tgt.box.x + tgt.box.w/2, tgt.box.y + tgt.box.h/2}
	sc := [2]float64{src.box.x + src.box.w/2, src.box.y + src.box.h/2}
	if len(wps) > 0 {
		tc, sc = wps[0], wps[len(wps)-1]
	}
	p0, d0 := portPoint(c, src, "exit", tc)
	p1, d1 := portPoint(c, tgt, "entry", sc)
	pts := [][2]float64{p0}
	pts = append(pts, wps...)
	pts = append(pts, p1)
	out := [][2]float64{pts[0]}
	axis := d0
	for i := 1; i < len(pts); i++ {
		a, b := out[len(out)-1], pts[i]
		switch {
		case a[0] == b[0] || a[1] == b[1]:
			out = append(out, b)
		case i == len(pts)-1 && d1 != axis:
			if axis == 'v' {
				out = append(out, [2]float64{a[0], b[1]}, b)
			} else {
				out = append(out, [2]float64{b[0], a[1]}, b)
			}
		case axis == 'v':
			my := (a[1] + b[1]) / 2
			out = append(out, [2]float64{a[0], my}, [2]float64{b[0], my}, b)
		default:
			mx := (a[0] + b[0]) / 2
			out = append(out, [2]float64{mx, a[1]}, [2]float64{mx, b[1]}, b)
		}
		if i < len(pts)-1 { // after a waypoint the next hop leaves in the other direction
			if axis == 'v' {
				axis = 'h'
			} else {
				axis = 'v'
			}
		}
	}
	return out
}

// portPoint is where an edge attaches: the explicit exitX/exitY (entryX/entryY) of the style, otherwise the box side that faces
// the other end. dir is 'v' when the edge leaves vertically, 'h' when horizontally.
func portPoint(c *Cell, sh *lintShape, which string, toward [2]float64) ([2]float64, byte) {
	fx, okx := c.Prop("st:" + which + "X")
	fy, oky := c.Prop("st:" + which + "Y")
	b := sh.box
	if okx && oky {
		x, y := parseNum(fx), parseNum(fy)
		dir := byte('v')
		if (x <= 0 || x >= 1) && y > 0 && y < 1 {
			dir = 'h'
		}
		return [2]float64{b.x + x*b.w, b.y + y*b.h}, dir
	}
	cx, cy := b.x+b.w/2, b.y+b.h/2
	dx, dy := toward[0]-cx, toward[1]-cy
	if math.Abs(dx)*b.h > math.Abs(dy)*b.w {
		if dx > 0 {
			return [2]float64{b.x + b.w, cy}, 'h'
		}
		return [2]float64{b.x, cy}, 'h'
	}
	if dy > 0 {
		return [2]float64{cx, b.y + b.h}, 'v'
	}
	return [2]float64{cx, b.y}, 'v'
}
