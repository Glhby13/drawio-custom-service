package model

import (
	"math"
	"sort"
)

// Hierarchical auto-layout (used by build_diagram and by layout algo "hierarchical").
//
// Why it exists: a language model that writes absolute x/y for every shape cannot see the result, so edges cross shapes and
// labels, containers overlap and spacing is uneven. Here the model only describes the graph; the server computes the geometry:
//
//   - Containers are laid out bottom-up (a container is sized to fit its content plus a title band).
//   - At every level the shapes are arranged in layers (longest path after breaking cycles), ordered by barycentre sweeps to
//     reduce edge crossings; edges that skip layers get dummy nodes, which become the edge's waypoints so it runs in the gutter
//     between shapes instead of through them.
//   - Edges leave and enter on the side that faces the flow, spread along that side so they do not share one point.
//
// Everything is deterministic (no maps iterated for output, no randomness), so the same input always gives the same picture.

const (
	layoutMargin  = 40.0
	layoutPad     = 28.0
	layoutTitle   = 44.0
	layoutNodeGap = 56.0
	layoutLayer   = 84.0
	layoutLayerLb = 120.0 // gutter that carries an edge label
	layoutDummyW  = 14.0
	layoutGrid    = 40.0
	groupMinW     = 160.0
	groupMinH     = 100.0
)

// LNode is one shape or container to place. Leaf shapes bring their size; a container's size is computed.
type LNode struct {
	ID, Parent string // Parent "" = top level
	W, H       float64
	Group      bool
	Title      string // container title (sets its minimum width)
}

// LEdge connects two nodes (leaf or container).
type LEdge struct {
	ID, From, To string
	Labelled     bool
}

// LOpts selects the flow direction: top-to-bottom by default, left-to-right when Horizontal.
type LOpts struct{ Horizontal bool }

// Route is how one edge should be drawn: waypoints (absolute) and the port on each end as a fraction of the shape's box.
type Route struct {
	Points [][2]float64
	Exit   [2]float64
	Entry  [2]float64
}

// LResult is the computed geometry. Pos is absolute for every node; Size is set for every node (computed for containers).
type LResult struct {
	Pos    map[string][2]float64
	Size   map[string][2]float64
	Routes map[string]Route
	// Strategy is "flat" (one layering for all shapes, containers drawn around them) or "nested" (container by container).
	Strategy string
}

type levelInfo struct {
	layer  map[string]int
	chains map[int][][2]float64 // edge index -> dummy-node centres, lower layer first, in the level's own coordinates
	gut    []float64            // gut[l]: coordinate along the flow of the gutter between layer l and l+1 (edges turn there)
}

type engine struct {
	nodes  map[string]*LNode
	kids   map[string][]string
	size   map[string][2]float64
	rel    map[string][2]float64 // position inside the parent's box
	h      bool
	edges  []LEdge
	ok     []bool
	ea, eb []string // endpoints lifted to the children of the edge's lowest common ancestor
	elca   []string
	lv     map[string]*levelInfo
	order  []string // all node ids in input order
}

// AutoLayout computes positions for nodes and routes for edges. Unknown endpoints and edges inside one container's own
// hierarchy (container to its child) are skipped.
func AutoLayout(nodes []LNode, edges []LEdge, o LOpts) *LResult {
	e := &engine{nodes: map[string]*LNode{}, kids: map[string][]string{}, size: map[string][2]float64{}, rel: map[string][2]float64{},
		h: o.Horizontal, edges: edges, lv: map[string]*levelInfo{}}
	for i := range nodes {
		n := nodes[i]
		e.nodes[n.ID] = &n
	}
	for _, n := range nodes { // input order is the tie-break everywhere
		e.order = append(e.order, n.ID)
		p := n.Parent
		if _, ok := e.nodes[p]; !ok {
			p = ""
		}
		e.kids[p] = append(e.kids[p], n.ID)
		if !n.Group {
			e.size[n.ID] = [2]float64{n.W, n.H}
		}
	}
	e.lift()
	strategy := "flat"
	if !e.flat() {
		strategy = "nested"
		e.lv = map[string]*levelInfo{} // a failed attempt leaves nothing behind
		e.rel = map[string][2]float64{}
		for id := range e.size {
			if e.nodes[id].Group {
				delete(e.size, id)
			}
		}
		e.fit("")
	}
	res := &LResult{Pos: map[string][2]float64{}, Size: map[string][2]float64{}, Routes: map[string]Route{}, Strategy: strategy}
	abs := map[string][2]float64{"": {0, 0}}
	var walk func(parent string)
	walk = func(parent string) {
		for _, id := range e.kids[parent] {
			r := e.rel[id]
			p := abs[parent]
			abs[id] = [2]float64{p[0] + r[0], p[1] + r[1]}
			res.Pos[id] = abs[id]
			res.Size[id] = e.size[id]
			walk(id)
		}
	}
	walk("")
	e.routes(res, abs)
	return res
}

func (e *engine) parentOf(id string) string {
	if n := e.nodes[id]; n != nil {
		if _, ok := e.nodes[n.Parent]; ok {
			return n.Parent
		}
	}
	return ""
}

// lift finds, for every edge, the lowest container that holds both ends and the two children of it that lead to them.
func (e *engine) lift() {
	n := len(e.edges)
	e.ok, e.ea, e.eb, e.elca = make([]bool, n), make([]string, n), make([]string, n), make([]string, n)
	chain := func(id string) []string {
		c := []string{id}
		for q, i := e.parentOf(id), 0; i < 64; i++ {
			c = append(c, q)
			if q == "" {
				break
			}
			q = e.parentOf(q)
		}
		return c
	}
	for i, ed := range e.edges {
		if e.nodes[ed.From] == nil || e.nodes[ed.To] == nil || ed.From == ed.To {
			continue
		}
		a, b := chain(ed.From), chain(ed.To)
		inB := map[string]int{}
		for j, id := range b {
			inB[id] = j
		}
		if _, anc := inB[a[0]]; anc && a[0] != "" { // from is an ancestor of to
			continue
		}
		for j := 1; j < len(a); j++ {
			if k, hit := inB[a[j]]; hit {
				if k == 0 { // to is an ancestor of from
					break
				}
				e.ok[i], e.ea[i], e.eb[i], e.elca[i] = true, a[j-1], b[k-1], a[j]
				break
			}
		}
	}
}

// bs / ds are the shape's size across and along the flow.
func (e *engine) bs(id string) float64 {
	s := e.size[id]
	if e.h {
		return s[1]
	}
	return s[0]
}
func (e *engine) ds(id string) float64 {
	s := e.size[id]
	if e.h {
		return s[0]
	}
	return s[1]
}

func (e *engine) shiftLevel(parent string, dx, dy float64) {
	for _, id := range e.kids[parent] {
		r := e.rel[id]
		e.rel[id] = [2]float64{r[0] + dx, r[1] + dy}
	}
	if li := e.lv[parent]; li != nil {
		for i := range li.gut {
			if e.h {
				li.gut[i] += dx
			} else {
				li.gut[i] += dy
			}
		}
		for k, pts := range li.chains {
			for i := range pts {
				pts[i][0] += dx
				pts[i][1] += dy
			}
			li.chains[k] = pts
		}
	}
}

// fit lays out a container's content (children first) and sizes the container; "" is the page.
func (e *engine) fit(group string) {
	ids := e.kids[group]
	for _, id := range ids {
		if e.nodes[id].Group {
			e.fit(id)
		}
	}
	if group == "" {
		if len(ids) > 0 {
			e.layoutLevel("", ids)
		}
		e.shiftLevel("", layoutMargin, layoutMargin)
		return
	}
	if len(ids) == 0 {
		e.size[group] = [2]float64{groupMinW, groupMinH}
		return
	}
	w, h := e.layoutLevel(group, ids)
	W, H := w+2*layoutPad, h+layoutTitle+layoutPad
	if t := TextWidth(e.nodes[group].Title, 13, true) + 48; W < t {
		W = t
	}
	W, H = math.Max(W, groupMinW), math.Max(H, groupMinH)
	e.shiftLevel(group, (W-w)/2, layoutTitle)
	e.size[group] = [2]float64{ceil10(W), ceil10(H)}
}

type lref struct {
	idx  int
	a, b string
}

// layoutLevel places the direct children of one container (or the page) and returns the content extent.
func (e *engine) layoutLevel(parent string, ids []string) (float64, float64) {
	var refs []lref
	for i := range e.edges {
		if e.ok[i] && e.elca[i] == parent {
			refs = append(refs, lref{i, e.ea[i], e.eb[i]})
		}
	}
	if len(refs) == 0 {
		e.lv[parent] = &levelInfo{layer: map[string]int{}, chains: map[int][][2]float64{}}
		return e.grid(ids)
	}
	return e.layoutCore(parent, ids, refs, nil)
}

// cluster tells layoutCore which shapes belong together: path[id] is the chain of container ranks (outermost first) of a shape,
// edgePath(i) the chain of the lowest container holding both ends of edge i.
type cluster struct {
	path     map[string][]int
	edgePath func(int) []int
	layer    map[string]int // when set, the layer of every shape is given (container bands) instead of computed
}

func cmpPath(pu, pv []int) int {
	for k := 0; ; k++ {
		ru, rv := -1, -1 // a shape directly in the outer container sorts before the inner containers
		if k < len(pu) {
			ru = pu[k]
		}
		if k < len(pv) {
			rv = pv[k]
		}
		if ru != rv {
			if ru < rv {
				return -1
			}
			return 1
		}
		if k >= len(pu) && k >= len(pv) {
			return 0
		}
	}
}

func commonPrefix(a, b []int) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// layoutCore is the layered layout. With cl != nil the shapes of one container stay contiguous in every layer and the gaps leave
// room for the container boxes that will be drawn around them.
func (e *engine) layoutCore(parent string, ids []string, refs []lref, cl *cluster) (float64, float64) {
	li := &levelInfo{layer: map[string]int{}, chains: map[int][][2]float64{}}
	e.lv[parent] = li
	n := len(ids)
	idx := make(map[string]int, n)
	for i, id := range ids {
		idx[id] = i
	}
	type pair struct{ a, b int }

	// 1. break cycles (depth-first; the edge that closes a cycle is reversed for layering only)
	succ := make([][]int, n)
	seen := map[pair]bool{}
	for _, r := range refs {
		a, b := idx[r.a], idx[r.b]
		if a == b || seen[pair{a, b}] {
			continue
		}
		seen[pair{a, b}] = true
		succ[a] = append(succ[a], b)
	}
	color := make([]int, n)
	rev := map[pair]bool{}
	var dfs func(u int)
	dfs = func(u int) {
		color[u] = 1
		for _, v := range succ[u] {
			switch color[v] {
			case 1:
				rev[pair{u, v}] = true
			case 0:
				dfs(v)
			}
		}
		color[u] = 2
	}
	for u := 0; u < n; u++ {
		if color[u] == 0 {
			dfs(u)
		}
	}
	dag := make([][]int, n)
	indeg := make([]int, n)
	dseen := map[pair]bool{}
	for u := range succ {
		for _, v := range succ[u] {
			a, b := u, v
			if rev[pair{u, v}] {
				a, b = v, u
			}
			if dseen[pair{a, b}] {
				continue
			}
			dseen[pair{a, b}] = true
			dag[a] = append(dag[a], b)
			indeg[b]++
		}
	}

	// 2. layers: longest path from the sources (or the container bands handed in by the caller)
	layer := make([]int, n)
	fixedLayers := cl != nil && cl.layer != nil
	if fixedLayers {
		for i, id := range ids {
			layer[i] = cl.layer[id]
		}
	}
	deg := append([]int(nil), indeg...)
	var queue []int
	for u := 0; u < n; u++ {
		if deg[u] == 0 {
			queue = append(queue, u)
		}
	}
	for len(queue) > 0 && !fixedLayers {
		u := queue[0]
		queue = queue[1:]
		for _, v := range dag[u] {
			if layer[u]+1 > layer[v] {
				layer[v] = layer[u] + 1
			}
			if deg[v]--; deg[v] == 0 {
				queue = append(queue, v)
			}
		}
	}
	for u := n - 1; u >= 0 && !fixedLayers; u-- { // pull a source down next to its first consumer: shorter edges
		if indeg[u] == 0 && len(dag[u]) > 0 {
			m := layer[dag[u][0]]
			for _, v := range dag[u] {
				if layer[v] < m {
					m = layer[v]
				}
			}
			if m-1 > layer[u] {
				layer[u] = m - 1
			}
		}
	}

	// 3. dummy nodes for edges that skip layers
	maxL := 0
	for _, l := range layer {
		if l > maxL {
			maxL = l
		}
	}
	nodeLayer := append([]int(nil), layer...)
	var up, down [][]int
	for i := 0; i < n; i++ {
		up, down = append(up, nil), append(down, nil)
	}
	link := func(u, v int) { down[u] = append(down[u], v); up[v] = append(up[v], u) }
	chains := map[int][]int{}
	dummyEdge := map[int]int{}
	labelGap := make([]bool, maxL+1)
	for _, r := range refs {
		a, b := idx[r.a], idx[r.b]
		if a == b || layer[a] == layer[b] {
			continue
		}
		lo, hi := a, b
		if layer[a] > layer[b] {
			lo, hi = b, a
		}
		prev := lo
		for l := layer[lo] + 1; l < layer[hi]; l++ {
			d := len(nodeLayer)
			nodeLayer = append(nodeLayer, l)
			up, down = append(up, nil), append(down, nil)
			link(prev, d)
			chains[r.idx] = append(chains[r.idx], d)
			dummyEdge[d] = r.idx
			prev = d
		}
		link(prev, hi)
		if e.edges[r.idx].Labelled {
			for g := layer[lo]; g < layer[hi]; g++ {
				labelGap[g] = true
			}
		}
	}
	N := len(nodeLayer)
	layers := make([][]int, maxL+1)
	for v := 0; v < N; v++ {
		layers[nodeLayer[v]] = append(layers[nodeLayer[v]], v)
	}

	pathOf := func(v int) []int {
		if cl == nil {
			return nil
		}
		if v < n {
			return cl.path[ids[v]]
		}
		return cl.edgePath(dummyEdge[v])
	}
	// 4. order within layers: barycentre sweeps
	pos := make([]int, N)
	setPos := func(l int) {
		for i, v := range layers[l] {
			pos[v] = i
		}
	}
	for l := range layers {
		setPos(l)
	}
	sweep := func(l int, nb [][]int) {
		key := make(map[int]float64, len(layers[l]))
		for _, v := range layers[l] {
			if nb == nil || len(nb[v]) == 0 {
				key[v] = float64(pos[v])
				continue
			}
			s := 0.0
			for _, u := range nb[v] {
				s += float64(pos[u])
			}
			key[v] = s / float64(len(nb[v]))
		}
		var mean map[int]float64
		if cl != nil { // a container's members move together: its position is the mean of its members'
			sum, cnt := map[int]float64{}, map[int]int{}
			for _, v := range layers[l] {
				if p := pathOf(v); len(p) > 0 {
					sum[p[0]] += key[v]
					cnt[p[0]]++
				}
			}
			mean = map[int]float64{}
			for r, t := range sum {
				mean[r] = t / float64(cnt[r])
			}
		}
		top := func(v int) float64 {
			if p := pathOf(v); len(p) > 0 {
				return mean[p[0]]
			}
			return key[v]
		}
		sort.SliceStable(layers[l], func(i, j int) bool {
			a, b := layers[l][i], layers[l][j]
			if cl != nil {
				if ta, tb := top(a), top(b); ta != tb {
					return ta < tb
				}
				if c := cmpPath(pathOf(a), pathOf(b)); c != 0 {
					return c < 0
				}
			}
			return key[a] < key[b]
		})
		setPos(l)
	}
	if cl != nil {
		for l := range layers {
			sweep(l, nil) // nb[v] is never read: pos is the key for a node without neighbours
		}
	}
	for it := 0; it < 12; it++ {
		if it%2 == 0 {
			for l := 1; l <= maxL; l++ {
				sweep(l, up)
			}
		} else {
			for l := maxL - 1; l >= 0; l-- {
				sweep(l, down)
			}
		}
	}

	// 5. coordinates across the flow
	bsz := func(v int) float64 {
		if v >= n {
			return layoutDummyW
		}
		return e.bs(ids[v])
	}
	gap := func(u, v int) float64 {
		base := layoutNodeGap
		if u >= n || v >= n {
			base = 28
		}
		if cl != nil { // every container boundary between the two shapes needs padding on both sides
			pu, pv := pathOf(u), pathOf(v)
			c := commonPrefix(pu, pv)
			base += float64(len(pu)-c+len(pv)-c) * layoutPad
		}
		return base
	}
	x := make([]float64, N)
	maxW := 0.0
	widths := make([]float64, maxL+1)
	for l := range layers {
		cur := 0.0
		for i, v := range layers[l] {
			if i > 0 {
				cur += gap(layers[l][i-1], v)
			}
			x[v] = cur
			cur += bsz(v)
		}
		widths[l] = cur
		maxW = math.Max(maxW, cur)
	}
	for l := range layers {
		for _, v := range layers[l] {
			x[v] += (maxW - widths[l]) / 2
		}
	}
	for pass := 0; pass < 10; pass++ {
		var order []int
		var nb [][]int
		if pass%2 == 0 {
			for l := 1; l <= maxL; l++ {
				order = append(order, l)
			}
			nb = up
		} else {
			for l := maxL - 1; l >= 0; l-- {
				order = append(order, l)
			}
			nb = down
		}
		for _, l := range order {
			vs := layers[l]
			want := make([]float64, len(vs))
			for i, v := range vs {
				if len(nb[v]) == 0 {
					want[i] = x[v]
					continue
				}
				s := 0.0
				for _, u := range nb[v] {
					s += x[u] + bsz(u)/2
				}
				want[i] = s/float64(len(nb[v])) - bsz(v)/2
			}
			f, g := make([]float64, len(vs)), make([]float64, len(vs))
			for i := range vs { // packed left, and packed right: both honour every gap, so their mean does too
				f[i] = want[i]
				if i > 0 {
					f[i] = math.Max(want[i], f[i-1]+bsz(vs[i-1])+gap(vs[i-1], vs[i]))
				}
			}
			for i := len(vs) - 1; i >= 0; i-- {
				g[i] = want[i]
				if i < len(vs)-1 {
					g[i] = math.Min(want[i], g[i+1]-bsz(vs[i])-gap(vs[i], vs[i+1]))
				}
			}
			for i, v := range vs {
				x[v] = (f[i] + g[i]) / 2
			}
		}
	}
	minX := math.Inf(1)
	for v := 0; v < N; v++ {
		minX = math.Min(minX, x[v])
	}
	W := 0.0
	for v := 0; v < N; v++ {
		x[v] -= minX
		W = math.Max(W, x[v]+bsz(v))
	}

	// 6. coordinates along the flow
	thick := make([]float64, maxL+1)
	for v := 0; v < n; v++ {
		thick[nodeLayer[v]] = math.Max(thick[nodeLayer[v]], e.ds(ids[v]))
	}
	extra := make([]float64, maxL+1) // room for container edges (and titles) between layer l and l+1
	closePad := make([]float64, maxL+1)
	if cl != nil {
		first, last := map[int]int{}, map[int]int{}
		for v := 0; v < n; v++ {
			for _, r := range pathOf(v) {
				if f, ok := first[r]; !ok || nodeLayer[v] < f {
					first[r] = nodeLayer[v]
				}
				if nodeLayer[v] > last[r] {
					last[r] = nodeLayer[v]
				}
			}
		}
		closeMax, openMax := make([]int, maxL+2), make([]int, maxL+2)
		for v := 0; v < n; v++ {
			l := nodeLayer[v]
			closing, opening := 0, 0
			for _, r := range pathOf(v) {
				if last[r] == l {
					closing++
				}
				if first[r] == l {
					opening++
				}
			}
			closeMax[l] = max(closeMax[l], closing)
			openMax[l] = max(openMax[l], opening)
		}
		for l := 0; l < maxL; l++ { // the closing edge of a container and the title of the next one share this gap
			closePad[l] = float64(closeMax[l]) * layoutPad
			extra[l] = closePad[l] + float64(openMax[l+1])*layoutTitle
		}
	}
	start := make([]float64, maxL+1)
	cur := 0.0
	for l := 0; l <= maxL; l++ {
		start[l] = cur
		g := layoutLayer
		if labelGap[l] {
			g = layoutLayerLb
		}
		cur += thick[l] + g + extra[l]
	}
	H := start[maxL] + thick[maxL]
	li.gut = make([]float64, maxL+1)
	for l := 0; l < maxL; l++ {
		base := layoutLayer
		if labelGap[l] {
			base = layoutLayerLb
		}
		li.gut[l] = start[l] + thick[l] + closePad[l] + base/2
	}
	xy := func(b, d float64) [2]float64 {
		if e.h {
			return [2]float64{d, b}
		}
		return [2]float64{b, d}
	}
	for v := 0; v < n; v++ {
		l := nodeLayer[v]
		e.rel[ids[v]] = xy(x[v], start[l]+(thick[l]-e.ds(ids[v]))/2)
		li.layer[ids[v]] = l
	}
	for k, ds := range chains {
		pts := make([][2]float64, 0, len(ds))
		for _, d := range ds {
			l := nodeLayer[d]
			pts = append(pts, xy(x[d]+layoutDummyW/2, start[l]+thick[l]/2))
		}
		li.chains[k] = pts
	}
	if e.h {
		return H, W
	}
	return W, H
}

// grid packs unconnected children into a near-square grid.
func (e *engine) grid(ids []string) (float64, float64) {
	cols := int(math.Ceil(math.Sqrt(float64(len(ids)))))
	if cols < 1 {
		cols = 1
	}
	x, y, rowH, maxX := 0.0, 0.0, 0.0, 0.0
	for i, id := range ids {
		s := e.size[id]
		e.rel[id] = [2]float64{x, y}
		x += s[0] + layoutGrid
		rowH = math.Max(rowH, s[1])
		maxX = math.Max(maxX, x-layoutGrid)
		if (i+1)%cols == 0 || i == len(ids)-1 {
			x, y, rowH = 0, y+rowH+layoutGrid, 0
		}
	}
	return maxX, y - layoutGrid
}

// routes derives ports and waypoints once every absolute position is known.
func (e *engine) routes(res *LResult, abs map[string][2]float64) {
	type end struct {
		edge  int
		src   bool
		other float64
	}
	sides := map[[2]string][]end{} // (node, side) -> ends on that side
	sideOf := make([][2]string, len(e.edges))
	centre := func(id string) (float64, float64) {
		p, s := abs[id], e.size[id]
		return p[0] + s[0]/2, p[1] + s[1]/2
	}
	for i, ed := range e.edges {
		if !e.ok[i] {
			continue
		}
		li := e.lv[e.elca[i]]
		la, lb := li.layer[e.ea[i]], li.layer[e.eb[i]]
		ax, ay := centre(ed.From)
		bx, by := centre(ed.To)
		var s, t string
		switch {
		case !e.h && la < lb:
			s, t = "bottom", "top"
		case !e.h && la > lb:
			s, t = "top", "bottom"
		case !e.h && ax <= bx:
			s, t = "right", "left"
		case !e.h:
			s, t = "left", "right"
		case la < lb:
			s, t = "right", "left"
		case la > lb:
			s, t = "left", "right"
		case ay <= by:
			s, t = "bottom", "top"
		default:
			s, t = "top", "bottom"
		}
		sideOf[i] = [2]string{s, t}
		across := func(side string, ox, oy float64) float64 {
			if side == "top" || side == "bottom" {
				return ox
			}
			return oy
		}
		sides[[2]string{ed.From, s}] = append(sides[[2]string{ed.From, s}], end{i, true, across(s, bx, by)})
		sides[[2]string{ed.To, t}] = append(sides[[2]string{ed.To, t}], end{i, false, across(t, ax, ay)})
	}
	port := map[[2]int][2]float64{} // (edge, 0 source / 1 target) -> fraction of the box
	keys := make([][2]string, 0, len(sides))
	for k := range sides {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		list := sides[k]
		sort.SliceStable(list, func(i, j int) bool { return list[i].other < list[j].other })
		side := k[1]
		for i, en := range list {
			f := float64(i+1) / float64(len(list)+1)
			var p [2]float64
			switch side {
			case "top":
				p = [2]float64{f, 0}
			case "bottom":
				p = [2]float64{f, 1}
			case "left":
				p = [2]float64{0, f}
			default:
				p = [2]float64{1, f}
			}
			which := 1
			if en.src {
				which = 0
			}
			port[[2]int{en.edge, which}] = p
		}
	}
	// several long edges share a gutter: give each its own lane, ordered so their turns do not cross each other's runs
	type longEdge struct {
		lo  int
		bs  []float64
		rev bool
	}
	long := map[int]longEdge{}
	type gk struct {
		lvl string
		g   int
	}
	inGutter := map[gk][]int{}
	for i, ed := range e.edges {
		if !e.ok[i] {
			continue
		}
		li := e.lv[e.elca[i]]
		if len(li.chains[i]) == 0 || li.gut == nil {
			continue
		}
		lo, bs, rev := e.breadths(li, li.chains[i], abs[e.elca[i]], e.ea[i], e.eb[i], ed, port[[2]int{i, 0}], port[[2]int{i, 1}], abs)
		long[i] = longEdge{lo, bs, rev}
		for j := 0; j+1 < len(bs); j++ {
			inGutter[gk{e.elca[i], lo + j}] = append(inGutter[gk{e.elca[i], lo + j}], i)
		}
	}
	laneOf := map[[3]int]float64{} // (edge, gutter) -> offset
	lvIdx := map[string]int{}
	for k, list := range inGutter {
		span := func(i int) (float64, float64) { le := long[i]; j := k.g - le.lo; return le.bs[j], le.bs[j+1] }
		in := func(x, a, c float64) bool { return x > math.Min(a, c)+0.5 && x < math.Max(a, c)-0.5 }
		// cost of drawing edge u above edge v in this gutter: u's drop at its far end hits v's run, v's rise at its near end hits u's run
		cost := func(u, v int) int {
			ua, uc := span(u)
			va, vc := span(v)
			n := 0
			if in(uc, va, vc) {
				n++
			}
			if in(va, ua, uc) {
				n++
			}
			return n
		}
		sort.SliceStable(list, func(x, y int) bool { a1, c1 := span(list[x]); a2, c2 := span(list[y]); return a1+c1 < a2+c2 })
		for pass := 0; pass < len(list); pass++ {
			changed := false
			for x := 0; x+1 < len(list); x++ {
				if cost(list[x+1], list[x]) < cost(list[x], list[x+1]) {
					list[x], list[x+1] = list[x+1], list[x]
					changed = true
				}
			}
			if !changed {
				break
			}
		}
		if _, ok := lvIdx[k.lvl]; !ok {
			lvIdx[k.lvl] = len(lvIdx)
		}
		for rank, i := range list {
			off := (float64(rank) - float64(len(list)-1)/2) * 8
			laneOf[[3]int{i, k.g, 0}] = math.Max(-34, math.Min(34, off))
		}
	}
	for i, ed := range e.edges {
		if !e.ok[i] {
			continue
		}
		li := e.lv[e.elca[i]]
		r := Route{Exit: port[[2]int{i, 0}], Entry: port[[2]int{i, 1}]}
		if pts := li.chains[i]; len(pts) > 0 && li.gut != nil {
			le, idx := long[i], i
			r.Points = e.corners(li, le.lo, le.bs, le.rev, abs[e.elca[i]], func(g int) float64 { return laneOf[[3]int{idx, g, 0}] })
		}
		res.Routes[ed.ID] = r
	}
}

// flat is the preferred strategy for diagrams with containers: every leaf shape goes through ONE layering, so an edge between
// two containers runs through the gutters between shapes (dummy waypoints) instead of through the shapes of its own container.
// The container boxes are then drawn around their members. If that would make boxes collide (containers whose members interleave
// across the flow), it reports false and the caller falls back to the nested, container-by-container layout.
func (e *engine) flat() bool {
	rank := map[string]int{}
	for _, id := range e.order {
		if e.nodes[id].Group {
			rank[id] = len(rank)
		}
	}
	if len(rank) == 0 {
		return false
	}
	var leaves []string
	path := map[string][]int{}
	for _, id := range e.order {
		if e.nodes[id].Group {
			continue
		}
		leaves = append(leaves, id)
		var rev []int
		for q := e.parentOf(id); q != ""; q = e.parentOf(q) {
			rev = append(rev, rank[q])
		}
		for i := len(rev) - 1; i >= 0; i-- {
			path[id] = append(path[id], rev[i])
		}
	}
	var refs []lref
	ok := make([]bool, len(e.edges))
	for i, ed := range e.edges {
		a, b := e.nodes[ed.From], e.nodes[ed.To]
		if a == nil || b == nil || ed.From == ed.To {
			continue
		}
		if a.Group || b.Group {
			return false // an edge to a container needs the container's box: nested layout handles that
		}
		ok[i] = true
		refs = append(refs, lref{i, ed.From, ed.To})
	}
	if len(refs) == 0 || len(leaves) == 0 {
		return false
	}
	cl := &cluster{path: path, layer: e.bands(leaves, refs), edgePath: func(i int) []int {
		pa, pb := path[e.edges[i].From], path[e.edges[i].To]
		return pa[:commonPrefix(pa, pb)]
	}}
	e.layoutCore("", leaves, refs, cl)

	// container boxes around their members, innermost first
	abs := map[string][2]float64{}
	for _, id := range leaves {
		abs[id] = e.rel[id]
	}
	box := map[string]rect{}
	var build func(g string) bool
	build = func(g string) bool {
		minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, k := range e.kids[g] {
			var r rect
			if e.nodes[k].Group {
				if !build(k) {
					return false
				}
				r = box[k]
			} else {
				s := e.size[k]
				r = rect{abs[k][0], abs[k][1], s[0], s[1]}
			}
			minX, minY = math.Min(minX, r.x), math.Min(minY, r.y)
			maxX, maxY = math.Max(maxX, r.x+r.w), math.Max(maxY, r.y+r.h)
		}
		if math.IsInf(minX, 1) { // an empty container has no members to wrap: let the nested layout size it
			return false
		}
		r := rect{minX - layoutPad, minY - layoutTitle, maxX - minX + 2*layoutPad, maxY - minY + layoutTitle + layoutPad}
		if t := TextWidth(e.nodes[g].Title, 13, true) + 48; r.w < t {
			r.w = t
		}
		box[g] = r
		return true
	}
	var tops []string
	for _, id := range e.order {
		if e.parentOf(id) == "" && e.nodes[id].Group {
			tops = append(tops, id)
			if !build(id) {
				return false
			}
		}
	}
	// collisions: sibling boxes, and boxes that swallow shapes of another container
	rectOf := func(id string) rect {
		if r, ok := box[id]; ok {
			return r
		}
		s := e.size[id]
		return rect{abs[id][0], abs[id][1], s[0], s[1]}
	}
	for _, id := range e.order {
		for _, o := range e.order {
			if id >= o || e.parentOf(id) != e.parentOf(o) {
				continue
			}
			if rectOf(id).overlap(rectOf(o)) > 0 {
				return false
			}
		}
	}
	for g := range box {
		for _, id := range leaves {
			if e.within(id, g) {
				continue
			}
			if box[g].overlap(rectOf(id)) > 0 {
				return false
			}
		}
	}

	// accept: shift so the top-left corner of everything sits on the margin, store positions relative to the parent box
	minX, minY := math.Inf(1), math.Inf(1)
	for _, id := range e.order {
		r := rectOf(id)
		minX, minY = math.Min(minX, r.x), math.Min(minY, r.y)
	}
	dx, dy := layoutMargin-minX, layoutMargin-minY
	for id, r := range box {
		box[id] = rect{r.x + dx, r.y + dy, ceil10(r.w), ceil10(r.h)}
		e.size[id] = [2]float64{box[id].w, box[id].h}
	}
	for _, id := range leaves {
		abs[id] = [2]float64{abs[id][0] + dx, abs[id][1] + dy}
	}
	absOf := func(id string) [2]float64 {
		if r, ok := box[id]; ok {
			return [2]float64{r.x, r.y}
		}
		return abs[id]
	}
	for _, id := range e.order {
		a, p := absOf(id), [2]float64{}
		if par := e.parentOf(id); par != "" {
			p = absOf(par)
		}
		e.rel[id] = [2]float64{a[0] - p[0], a[1] - p[1]}
	}
	li := e.lv[""]
	for i := range li.gut {
		if e.h {
			li.gut[i] += dx
		} else {
			li.gut[i] += dy
		}
	}
	for k, pts := range li.chains {
		for i := range pts {
			pts[i][0] += dx
			pts[i][1] += dy
		}
		li.chains[k] = pts
	}
	for i, ed := range e.edges {
		e.ok[i], e.ea[i], e.eb[i], e.elca[i] = ok[i], ed.From, ed.To, ""
	}
	_ = tops
	return true
}

// within reports whether id is g or inside g.
func (e *engine) within(id, g string) bool {
	for q, n := id, 0; q != "" && n < 64; n++ {
		if q == g {
			return true
		}
		q = e.parentOf(q)
	}
	return false
}

// layerize gives every item the longest-path position from the sources, where item v occupies w[v] layers. Cycles are broken by
// reversing the edge that closes them (depth-first, input order), so the result is deterministic.
func layerize(n int, pairs [][2]int, w []int) []int {
	succ := make([][]int, n)
	seen := map[[2]int]bool{}
	for _, p := range pairs {
		if p[0] != p[1] && !seen[p] {
			seen[p] = true
			succ[p[0]] = append(succ[p[0]], p[1])
		}
	}
	color := make([]int, n)
	rev := map[[2]int]bool{}
	var dfs func(u int)
	dfs = func(u int) {
		color[u] = 1
		for _, v := range succ[u] {
			switch color[v] {
			case 1:
				rev[[2]int{u, v}] = true
			case 0:
				dfs(v)
			}
		}
		color[u] = 2
	}
	for u := 0; u < n; u++ {
		if color[u] == 0 {
			dfs(u)
		}
	}
	dag := make([][]int, n)
	indeg := make([]int, n)
	dseen := map[[2]int]bool{}
	for u := range succ {
		for _, v := range succ[u] {
			a, b := u, v
			if rev[[2]int{u, v}] {
				a, b = v, u
			}
			if !dseen[[2]int{a, b}] {
				dseen[[2]int{a, b}] = true
				dag[a] = append(dag[a], b)
				indeg[b]++
			}
		}
	}
	start := make([]int, n)
	var queue []int
	for u := 0; u < n; u++ {
		if indeg[u] == 0 {
			queue = append(queue, u)
		}
	}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, v := range dag[u] {
			if start[u]+w[u] > start[v] {
				start[v] = start[u] + w[u]
			}
			if indeg[v]--; indeg[v] == 0 {
				queue = append(queue, v)
			}
		}
	}
	return start
}

// bands assigns layers so that each top-level container (or loose shape) owns a contiguous band of layers: the containers end up
// stacked along the flow instead of interleaved, which is what keeps their boxes from colliding. Inside a band shapes are layered
// by their own edges. Edges between bands become long edges with waypoints in the gutters.
func (e *engine) bands(leaves []string, refs []lref) map[string]int {
	block := func(id string) string {
		top := id
		for q := e.parentOf(id); q != ""; q = e.parentOf(q) {
			top = q
		}
		return top
	}
	var blocks []string
	bi := map[string]int{}
	members := map[string][]string{}
	for _, id := range leaves {
		b := block(id)
		if _, ok := bi[b]; !ok {
			bi[b] = len(blocks)
			blocks = append(blocks, b)
		}
		members[b] = append(members[b], id)
	}
	local := map[string]int{}
	height := make([]int, len(blocks))
	for _, b := range blocks {
		idx := map[string]int{}
		for i, id := range members[b] {
			idx[id] = i
		}
		var pairs [][2]int
		for _, r := range refs {
			a, ok1 := idx[r.a]
			c, ok2 := idx[r.b]
			if ok1 && ok2 {
				pairs = append(pairs, [2]int{a, c})
			}
		}
		w := make([]int, len(members[b]))
		for i := range w {
			w[i] = 1
		}
		ls := layerize(len(members[b]), pairs, w)
		for i, id := range members[b] {
			local[id] = ls[i]
			height[bi[b]] = max(height[bi[b]], ls[i]+1)
		}
	}
	var bp [][2]int
	for _, r := range refs {
		if a, c := bi[block(r.a)], bi[block(r.b)]; a != c {
			bp = append(bp, [2]int{a, c})
		}
	}
	start := layerize(len(blocks), bp, height)
	used := map[int]bool{}
	layer := map[string]int{}
	for _, id := range leaves {
		layer[id] = start[bi[block(id)]] + local[id]
		used[layer[id]] = true
	}
	var ls []int
	for l := range used {
		ls = append(ls, l)
	}
	sort.Ints(ls) // squeeze out layers nobody sits on
	rank := map[int]int{}
	for i, l := range ls {
		rank[l] = i
	}
	for id, l := range layer {
		layer[id] = rank[l]
	}
	return layer
}

// breadths lists where a long edge sits across the flow at each stage: its start port, one entry per skipped layer (the dummy
// column) and its end port. lo is the lower of the two end layers; rev reports that the edge runs against the flow.
func (e *engine) breadths(li *levelInfo, dummies [][2]float64, origin [2]float64, a, b string, ed LEdge, exit, entry [2]float64, abs map[string][2]float64) (int, []float64, bool) {
	portAt := func(id string, f [2]float64) [2]float64 {
		p, s := abs[id], e.size[id]
		return [2]float64{p[0] + f[0]*s[0], p[1] + f[1]*s[1]}
	}
	src, dst := portAt(ed.From, exit), portAt(ed.To, entry)
	la, lb := li.layer[a], li.layer[b]
	lo, plo, phi := la, src, dst
	if la > lb {
		lo, plo, phi = lb, dst, src
	}
	across := func(p [2]float64) float64 { return p[0] }
	if e.h {
		across = func(p [2]float64) float64 { return p[1] }
	}
	bs := []float64{across(plo)}
	for _, d := range dummies {
		bs = append(bs, across([2]float64{d[0] + origin[0], d[1] + origin[1]}))
	}
	return lo, append(bs, across(phi)), la > lb
}

// corners turns those stages into the full list of bends: along its own column through every layer it skips and across only in
// the gutters between layers, so no part of it can touch a shape.
func (e *engine) corners(li *levelInfo, lo int, bs []float64, rev bool, origin [2]float64, lane func(int) float64) [][2]float64 {
	along := origin[1]
	if e.h {
		along = origin[0]
	}
	at := func(bv, d float64) [2]float64 {
		if e.h {
			return [2]float64{d, bv}
		}
		return [2]float64{bv, d}
	}
	var out [][2]float64
	for j := 0; j+1 < len(bs); j++ {
		g := li.gut[lo+j] + along + lane(lo+j)
		out = append(out, at(bs[j], g), at(bs[j+1], g))
	}
	if rev {
		for x, y := 0, len(out)-1; x < y; x, y = x+1, y-1 {
			out[x], out[y] = out[y], out[x]
		}
	}
	return out
}
