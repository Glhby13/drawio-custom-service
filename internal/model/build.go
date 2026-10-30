package model

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// BuildSpec is a declarative description of a diagram: the caller names the things and how they connect, the server decides
// geometry (see autolayout.go). It is the input of the MCP tool build_diagram.
type BuildSpec struct {
	Direction string       `json:"direction"` // "TB" (default) or "LR"
	Groups    []BuildGroup `json:"groups"`
	Nodes     []BuildNode  `json:"nodes"`
	Edges     []BuildEdge  `json:"edges"`
}

// BuildGroup is a container (a boundary, a namespace, a tier) that holds nodes and other groups.
type BuildGroup struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Parent string `json:"parent"`
	Color  string `json:"color"`
}

// BuildNode is one shape. Width/height are optional: by default the size follows the label.
type BuildNode struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Group  string  `json:"group"`
	Kind   string  `json:"kind"`
	Color  string  `json:"color"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// BuildEdge is a directed connection between two node or group ids.
type BuildEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Label  string `json:"label"`
	Dashed bool   `json:"dashed"`
}

// BuildReport describes what BuildOps produced.
type BuildReport struct {
	Groups, Nodes, Edges int
	Width, Height        float64
}

// Limits keep one build inside one atomic batch (500 ops) and keep the layout fast.
const (
	MaxBuildCells = 480
	maxGroupDepth = 4
	maxLabelRunes = 300
	maxNodeSide   = 600.0
)

type palette struct{ fill, stroke string }

var colors = map[string]palette{
	"blue": {"#dae8fc", "#6c8ebf"}, "green": {"#d5e8d4", "#82b366"}, "orange": {"#ffe6cc", "#d79b00"}, "red": {"#f8cecc", "#b85450"},
	"purple": {"#e1d5e7", "#9673a6"}, "gray": {"#f5f5f5", "#666666"}, "yellow": {"#fff2cc", "#d6b656"}, "teal": {"#d5f0ee", "#3a9d92"},
}

// kindStyle is the shape style per node kind; size hints widen/heighten shapes whose text area is smaller than their box.
var kindStyle = map[string]struct {
	style        string
	minW, minH   float64
	growW, growH float64
}{
	"box":      {"rounded=1;arcSize=12;", 120, 56, 1, 1},
	"service":  {"rounded=1;arcSize=24;", 130, 60, 1, 1},
	"database": {"shape=cylinder3;boundedLbl=1;backgroundOutline=1;size=12;", 130, 80, 1, 1.25},
	"queue":    {"shape=hexagon;perimeter=hexagonPerimeter2;fixedSize=1;size=16;", 150, 60, 1.2, 1},
	"actor":    {"rounded=1;arcSize=50;", 120, 50, 1, 1},
	"cloud":    {"shape=cloud;", 170, 90, 1.4, 1.5},
	"external": {"rounded=1;arcSize=12;dashed=1;dashPattern=6 4;", 120, 56, 1, 1},
	"decision": {"rhombus;", 140, 90, 1.7, 1.9},
	"note":     {"shape=note;size=14;align=left;spacingLeft=6;", 140, 60, 1, 1},
	"document": {"shape=document;boundedLbl=1;", 130, 70, 1, 1.2},
}

// Kinds and Colors list the accepted values (used in error messages and tool schemas).
func Kinds() []string {
	k := make([]string, 0, len(kindStyle))
	for n := range kindStyle {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}
func Colors() []string {
	k := make([]string, 0, len(colors))
	for n := range colors {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

// BuildError is a problem with the spec, listing every issue found so the caller can fix them in one go.
type BuildError struct{ Issues []string }

func (e *BuildError) Error() string {
	more := ""
	if len(e.Issues) > 12 {
		more = fmt.Sprintf(" (+%d more)", len(e.Issues)-12)
		return strings.Join(e.Issues[:12], "; ") + more
	}
	return strings.Join(e.Issues, "; ")
}

func styleToProps(style string, into map[string]string) {
	var names []string
	for _, tok := range strings.Split(style, ";") {
		if tok == "" {
			continue
		}
		if i := strings.IndexByte(tok, '='); i > 0 {
			into["st:"+tok[:i]] = tok[i+1:]
		} else {
			names = append(names, tok)
		}
	}
	if len(names) > 0 {
		into["sn"] = strings.Join(names, ";")
	}
}

func fnum(f float64) string { return Num(Round4(f)) }

// BuildOps validates the spec, lays it out and returns addCell operations (containers first, then shapes, then edges).
// page/layer say where the new cells go; existing lists cell ids already on the page (new ids must not collide).
func BuildOps(spec *BuildSpec, page, layer string, existing map[string]bool, idPrefix string) ([]Op, *BuildReport, error) {
	var issues []string
	bad := func(f string, a ...any) { issues = append(issues, fmt.Sprintf(f, a...)) }
	dir := strings.ToUpper(strings.TrimSpace(spec.Direction))
	if dir == "" {
		dir = "TB"
	}
	if dir != "TB" && dir != "LR" {
		bad("direction must be TB or LR, got %q", spec.Direction)
	}
	total := len(spec.Groups) + len(spec.Nodes) + len(spec.Edges)
	switch {
	case len(spec.Nodes)+len(spec.Groups) == 0:
		bad("nothing to build: give at least one node")
	case total > MaxBuildCells:
		bad("too many cells (%d groups + %d nodes + %d edges = %d, max %d per call): split the diagram over several calls or pages", len(spec.Groups), len(spec.Nodes), len(spec.Edges), total, MaxBuildCells)
	}
	known := map[string]bool{}
	isGroup := map[string]bool{}
	parentOf := map[string]string{}
	checkID := func(kind, id string) bool {
		switch {
		case !idRe.MatchString(id):
			bad("%s id %q must be 1-128 characters of letters, digits, _ . : -", kind, id)
		case known[id]:
			bad("duplicate id %q", id)
		case existing[id]:
			bad("id %q already exists on this page: choose another id", id)
		default:
			known[id] = true
			return true
		}
		return false
	}
	checkLabel := func(what, l string) {
		if runeLen(l) > maxLabelRunes {
			bad("%s label is longer than %d characters", what, maxLabelRunes)
		}
	}
	checkColor := func(what, c string) {
		if c == "" {
			return
		}
		if _, ok := colors[c]; !ok {
			bad("%s color %q is not one of %s", what, c, strings.Join(Colors(), ", "))
		}
	}
	for _, g := range spec.Groups {
		if checkID("group", g.ID) {
			isGroup[g.ID] = true
			parentOf[g.ID] = g.Parent
		}
		checkLabel("group "+g.ID, g.Label)
		checkColor("group "+g.ID, g.Color)
	}
	for _, g := range spec.Groups {
		if g.Parent != "" && !isGroup[g.Parent] {
			bad("group %q has parent %q which is not a group", g.ID, g.Parent)
		}
		d := 1
		for q := g.Parent; q != "" && d <= maxGroupDepth+1; q = parentOf[q] {
			d++
			if q == g.ID {
				bad("group %q is its own ancestor", g.ID)
				break
			}
		}
		if d > maxGroupDepth {
			bad("group %q is nested deeper than %d levels", g.ID, maxGroupDepth)
		}
	}
	for _, n := range spec.Nodes {
		checkID("node", n.ID)
		checkLabel("node "+n.ID, n.Label)
		checkColor("node "+n.ID, n.Color)
		if n.Kind != "" {
			if _, ok := kindStyle[n.Kind]; !ok {
				bad("node %q kind %q is not one of %s", n.ID, n.Kind, strings.Join(Kinds(), ", "))
			}
		}
		if n.Group != "" && !isGroup[n.Group] {
			bad("node %q is in group %q which does not exist", n.ID, n.Group)
		}
		if n.Width < 0 || n.Height < 0 || n.Width > maxNodeSide || n.Height > maxNodeSide || math.IsNaN(n.Width) || math.IsNaN(n.Height) {
			bad("node %q size must be between 0 and %.0f", n.ID, maxNodeSide)
		}
	}
	for i, ed := range spec.Edges {
		if !known[ed.From] || !known[ed.To] {
			bad("edge %d (%q -> %q) refers to an id that does not exist", i+1, ed.From, ed.To)
		}
		if ed.From == ed.To && ed.From != "" {
			bad("edge %d connects %q to itself", i+1, ed.From)
		}
		checkLabel(fmt.Sprintf("edge %d", i+1), ed.Label)
	}
	if len(issues) > 0 {
		return nil, nil, &BuildError{issues}
	}

	// sizes
	var ln []LNode
	for _, g := range spec.Groups {
		ln = append(ln, LNode{ID: g.ID, Parent: g.Parent, Group: true, Title: g.Label})
	}
	nodeStyle := map[string]string{}
	for _, n := range spec.Nodes {
		kind := n.Kind
		if kind == "" {
			kind = "box"
		}
		ks := kindStyle[kind]
		tw, th, _ := TextBlock(n.Label, 220, 12, false)
		w := math.Max(ks.minW, ceil10((tw+36)*ks.growW))
		h := math.Max(ks.minH, ceil10((th+26)*ks.growH))
		if n.Width > 0 {
			w = n.Width
		}
		if n.Height > 0 {
			h = n.Height
		}
		ln = append(ln, LNode{ID: n.ID, Parent: n.Group, W: w, H: h})
		nodeStyle[n.ID] = kind
	}
	var le []LEdge
	for i, ed := range spec.Edges {
		le = append(le, LEdge{ID: fmt.Sprintf("%se%d", idPrefix, i+1), From: ed.From, To: ed.To, Labelled: ed.Label != ""})
	}
	res := AutoLayout(ln, le, LOpts{Horizontal: dir == "LR"})

	rel := func(id, parent string) (float64, float64) {
		p := res.Pos[id]
		if parent == "" {
			return p[0], p[1]
		}
		q := res.Pos[parent]
		return p[0] - q[0], p[1] - q[1]
	}
	var ops []Op
	add := func(cell, kind, parent string, props map[string]string) {
		ops = append(ops, Op{ID: fmt.Sprintf("%so%d", idPrefix, len(ops)+1), T: OpAddCell, Page: page, Cell: cell, Kind: kind, Parent: parent, Props: props})
	}
	// containers: outer ones first so a parent always exists before its children
	depth := func(id string) int {
		d := 0
		for q := parentOf[id]; q != ""; q = parentOf[q] {
			d++
		}
		return d
	}
	groups := append([]BuildGroup(nil), spec.Groups...)
	sort.SliceStable(groups, func(i, j int) bool { return depth(groups[i].ID) < depth(groups[j].ID) })
	for _, g := range groups {
		c := colors[g.Color]
		if g.Color == "" {
			c = colors["gray"]
		}
		x, y := rel(g.ID, g.Parent)
		sz := res.Size[g.ID]
		props := map[string]string{"v": htmlLabel(g.Label), "gx": fnum(x), "gy": fnum(y), "gw": fnum(sz[0]), "gh": fnum(sz[1])}
		styleToProps(fmt.Sprintf("rounded=1;arcSize=4;whiteSpace=wrap;html=1;container=1;collapsible=0;recursiveResize=0;verticalAlign=top;align=left;spacingLeft=14;spacingTop=6;fontStyle=1;fontSize=13;fontColor=#1f2937;fillColor=%s;fillOpacity=45;strokeColor=%s;dashed=1;dashPattern=8 4;strokeWidth=1.5", c.fill, c.stroke), props)
		parent := layer
		if g.Parent != "" {
			parent = g.Parent
		}
		add(g.ID, "v", parent, props)
	}
	for _, n := range spec.Nodes {
		c := colors[n.Color]
		if n.Color == "" {
			c = colors["blue"]
		}
		x, y := rel(n.ID, n.Group)
		sz := res.Size[n.ID]
		props := map[string]string{"v": htmlLabel(n.Label), "gx": fnum(x), "gy": fnum(y), "gw": fnum(sz[0]), "gh": fnum(sz[1])}
		styleToProps(kindStyle[nodeStyle[n.ID]].style+fmt.Sprintf("whiteSpace=wrap;html=1;fontSize=12;fontColor=#1f2937;fillColor=%s;strokeColor=%s;strokeWidth=1.5", c.fill, c.stroke), props)
		parent := layer
		if n.Group != "" {
			parent = n.Group
		}
		add(n.ID, "v", parent, props)
	}
	for i, ed := range spec.Edges {
		id := le[i].ID
		rt := res.Routes[id]
		props := map[string]string{"src": ed.From, "tgt": ed.To, "gx": "0", "gy": "0", "gw": "0", "gh": "0", "grel": "1"}
		if ed.Label != "" {
			props["v"] = htmlLabel(ed.Label)
		}
		EdgeStyleProps(props, rt, ed.Dashed)
		add(id, "e", layer, props)
	}
	rep := &BuildReport{Groups: len(spec.Groups), Nodes: len(spec.Nodes), Edges: len(spec.Edges)}
	for _, id := range sortedKeys(res.Pos) {
		p, s := res.Pos[id], res.Size[id]
		rep.Width, rep.Height = math.Max(rep.Width, p[0]+s[0]), math.Max(rep.Height, p[1]+s[1])
	}
	return ops, rep, nil
}

// EdgeStyleProps writes the orthogonal edge style, the ports of a route and its waypoints into a cell's props.
func EdgeStyleProps(props map[string]string, rt Route, dashed bool) {
	st := "edgeStyle=orthogonalEdgeStyle;rounded=1;orthogonalLoop=1;jettySize=auto;html=1;endArrow=block;endFill=1;strokeWidth=1.5;strokeColor=#6b7280;fontSize=11;fontColor=#374151;labelBackgroundColor=default"
	if dashed {
		st += ";dashed=1;dashPattern=6 4"
	}
	styleToProps(st, props)
	if rt.Exit != ([2]float64{}) || rt.Entry != ([2]float64{}) {
		props["st:exitX"], props["st:exitY"] = fnum(rt.Exit[0]), fnum(rt.Exit[1])
		props["st:entryX"], props["st:entryY"] = fnum(rt.Entry[0]), fnum(rt.Entry[1])
		props["st:exitDx"], props["st:exitDy"], props["st:entryDx"], props["st:entryDy"] = "0", "0", "0", "0"
	}
	if len(rt.Points) > 0 {
		var b strings.Builder
		b.WriteString(`{"p":[`)
		for i, p := range rt.Points {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString("[" + fnum(p[0]) + "," + fnum(p[1]) + "]")
		}
		b.WriteString("]}")
		props["gex"] = b.String()
	}
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}
