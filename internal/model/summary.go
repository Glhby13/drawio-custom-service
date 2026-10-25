package model

import (
	"encoding/json"
	"html"
	"regexp"
	"sort"
	"strings"
)

// CellSummary is the agent/MCP-friendly view of a cell (no registers, no XML).
type CellSummary struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"` // vertex | edge | layer
	Parent string            `json:"parent"`
	Label  string            `json:"label,omitempty"`
	Style  map[string]string `json:"style,omitempty"`
	Names  string            `json:"styleNames,omitempty"`
	X      float64           `json:"x"`
	Y      float64           `json:"y"`
	W      float64           `json:"w,omitempty"`
	H      float64           `json:"h,omitempty"`
	Source string            `json:"source,omitempty"`
	Target string            `json:"target,omitempty"`
	Attrs  map[string]string `json:"attrs,omitempty"`
}

// PageSummary lists the cells of one page.
type PageSummary struct {
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Cells []CellSummary `json:"cells"`
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// PlainLabel strips HTML from a draw.io label.
func PlainLabel(s string) string {
	s = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</div>", "\n", "</p>", "\n").Replace(s)
	return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(s, "")))
}

func cellLabel(c *Cell) string {
	if v, ok := c.Prop("v"); ok {
		return PlainLabel(v)
	}
	if v, ok := c.Prop("va:label"); ok {
		return PlainLabel(v)
	}
	return ""
}

func styleMap(c *Cell) map[string]string {
	m := map[string]string{}
	for k, r := range c.Props {
		if strings.HasPrefix(k, "st:") && !r.Del {
			m[k[3:]] = r.V
		}
	}
	return m
}

// Summary returns the page/cell overview an AI agent works with (absolute positions, readable labels).
func (d *Doc) Summary() []PageSummary {
	var out []PageSummary
	for _, p := range d.SortedPages() {
		ps := PageSummary{ID: p.ID, Name: p.Name.V, Cells: []CellSummary{}}
		idx := p.childIndex()
		var walk func(parent string)
		walk = func(parent string) {
			for _, c := range idx[parent] {
				kind := map[string]string{"v": "vertex", "e": "edge", "-": "layer"}[c.Kind]
				cs := CellSummary{ID: c.ID, Kind: kind, Parent: parent, Label: cellLabel(c), Style: styleMap(c)}
				if len(cs.Style) == 0 {
					cs.Style = nil
				}
				cs.Names, _ = c.Prop("sn")
				if _, has := c.Prop("gw"); has {
					cs.X, cs.Y = p.Abs(c)
					cs.W, cs.H = c.num("gw"), c.num("gh")
				}
				cs.Source, _ = c.Prop("src")
				cs.Target, _ = c.Prop("tgt")
				for k, r := range c.Props {
					if strings.HasPrefix(k, "va:") && !r.Del && k != "va:label" {
						if cs.Attrs == nil {
							cs.Attrs = map[string]string{}
						}
						cs.Attrs[k[3:]] = r.V
					}
				}
				ps.Cells = append(ps.Cells, cs)
				walk(c.ID)
			}
		}
		walk("0")
		out = append(out, ps)
	}
	return out
}

// SummaryJSON is Summary marshalled.
func (d *Doc) SummaryJSON() []byte { b, _ := json.Marshal(d.Summary()); return b }

// ---- SVG export (server-side; draw.io's own export server is not part of the image) -------------------------------

func esvg(s string) string { return html.EscapeString(s) }

func colorOr(m map[string]string, k, def string) string {
	if v := m[k]; v != "" && v != "none" && v != "default" {
		return v
	}
	if m[k] == "none" {
		return "none"
	}
	return def
}

// SVG renders one page (the first when pageID is empty) as a standalone SVG: shapes, labels and straight/poly edges.
func (d *Doc) SVG(pageID string) string {
	pages := d.SortedPages()
	var pg *Page
	for _, p := range pages {
		if pageID == "" || p.ID == pageID {
			pg = p
			break
		}
	}
	if pg == nil {
		return `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="60"><text x="10" y="30">empty</text></svg>`
	}
	minX, minY, maxX, maxY := 1e9, 1e9, -1e9, -1e9
	type box struct{ x, y, w, h float64 }
	boxes := map[string]box{}
	var cells []*Cell
	idx := pg.childIndex()
	var walk func(parent string)
	walk = func(parent string) {
		for _, c := range idx[parent] {
			cells = append(cells, c)
			if _, has := c.Prop("gw"); has && c.Kind == "v" {
				x, y := pg.Abs(c)
				w, h := c.num("gw"), c.num("gh")
				boxes[c.ID] = box{x, y, w, h}
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x+w > maxX {
					maxX = x + w
				}
				if y+h > maxY {
					maxY = y + h
				}
			}
			walk(c.ID)
		}
	}
	walk("0")
	if len(boxes) == 0 {
		return `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="60"><text x="10" y="30" font-family="sans-serif" font-size="12">empty page</text></svg>`
	}
	const pad = 20.0
	ox, oy := minX-pad, minY-pad
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="` + Num(maxX-minX+2*pad) + `" height="` + Num(maxY-minY+2*pad) + `" viewBox="0 0 ` + Num(maxX-minX+2*pad) + ` ` + Num(maxY-minY+2*pad) + `" font-family="Helvetica,Arial,sans-serif" font-size="12">`)
	b.WriteString(`<defs><marker id="arr" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="8" markerHeight="8" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="#333"/></marker></defs>`)
	b.WriteString(`<rect width="100%" height="100%" fill="#ffffff"/>`)
	centre := func(id string) (float64, float64, bool) {
		bx, ok := boxes[id]
		return bx.x - ox + bx.w/2, bx.y - oy + bx.h/2, ok
	}
	for _, c := range cells {
		switch c.Kind {
		case "e":
			sx, sy, ok1 := centre(c.srcID())
			tx, ty, ok2 := centre(c.tgtID())
			if !ok1 || !ok2 {
				continue
			}
			st := styleMap(c)
			pts := []string{Num(sx) + "," + Num(sy)}
			if raw, ok := c.Prop("gex"); ok {
				var ex struct {
					P [][2]float64 `json:"p"`
				}
				if json.Unmarshal([]byte(raw), &ex) == nil {
					for _, q := range ex.P {
						pts = append(pts, Num(q[0]-ox)+","+Num(q[1]-oy))
					}
				}
			}
			pts = append(pts, Num(tx)+","+Num(ty))
			dash := ""
			if st["dashed"] == "1" {
				dash = ` stroke-dasharray="6 4"`
			}
			end := ` marker-end="url(#arr)"`
			if st["endArrow"] == "none" {
				end = ""
			}
			b.WriteString(`<polyline points="` + strings.Join(pts, " ") + `" fill="none" stroke="` + esvg(colorOr(st, "strokeColor", "#333333")) + `" stroke-width="1.5"` + dash + end + `/>`)
			if l := cellLabel(c); l != "" {
				b.WriteString(`<text x="` + Num((sx+tx)/2) + `" y="` + Num((sy+ty)/2-4) + `" text-anchor="middle" fill="#333">` + esvg(l) + `</text>`)
			}
		case "v":
			bx, ok := boxes[c.ID]
			if !ok {
				continue
			}
			st := styleMap(c)
			names, _ := c.Prop("sn")
			x, y := bx.x-ox, bx.y-oy
			fill, stroke := colorOr(st, "fillColor", "#ffffff"), colorOr(st, "strokeColor", "#000000")
			attrs := ` fill="` + esvg(fill) + `" stroke="` + esvg(stroke) + `" stroke-width="1.5"`
			switch {
			case strings.Contains(names, "ellipse") || st["shape"] == "ellipse":
				b.WriteString(`<ellipse cx="` + Num(x+bx.w/2) + `" cy="` + Num(y+bx.h/2) + `" rx="` + Num(bx.w/2) + `" ry="` + Num(bx.h/2) + `"` + attrs + `/>`)
			case strings.Contains(names, "rhombus") || st["shape"] == "rhombus":
				b.WriteString(`<polygon points="` + Num(x+bx.w/2) + `,` + Num(y) + ` ` + Num(x+bx.w) + `,` + Num(y+bx.h/2) + ` ` + Num(x+bx.w/2) + `,` + Num(y+bx.h) + ` ` + Num(x) + `,` + Num(y+bx.h/2) + `"` + attrs + `/>`)
			case strings.Contains(names, "text") || st["text"] != "" || (st["strokeColor"] == "none" && st["fillColor"] == "none"):
				// plain text: no outline
			default:
				rx := ""
				if st["rounded"] == "1" {
					rx = ` rx="8"`
				}
				b.WriteString(`<rect x="` + Num(x) + `" y="` + Num(y) + `" width="` + Num(bx.w) + `" height="` + Num(bx.h) + `"` + rx + attrs + `/>`)
			}
			if l := cellLabel(c); l != "" {
				lines := strings.Split(l, "\n")
				fc := colorOr(st, "fontColor", "#000000")
				start := y + bx.h/2 - float64(len(lines)-1)*7 + 4
				for i, ln := range lines {
					b.WriteString(`<text x="` + Num(x+bx.w/2) + `" y="` + Num(start+float64(i)*14) + `" text-anchor="middle" fill="` + esvg(fc) + `">` + esvg(ln) + `</text>`)
				}
			}
		}
	}
	b.WriteString("</svg>")
	return b.String()
}

func (c *Cell) srcID() string { v, _ := c.Prop("src"); return v }
func (c *Cell) tgtID() string { v, _ := c.Prop("tgt"); return v }

// ---- layout (used by the MCP layout tool; produces setPos ops, never touches the document itself) --------------------

// LayoutOps arranges the top-level vertices of a page. algo: "layered" (top-down by edges) or "grid".
func (d *Doc) LayoutOps(pageID, algo string, idPrefix string) ([]Op, error) {
	pg := d.Pages[pageID]
	if pg == nil || pg.Dead != 0 {
		return nil, errPage
	}
	var layer string
	for _, c := range pg.Children("0") {
		layer = c.ID
		break
	}
	verts := []*Cell{}
	for _, c := range pg.Children(layer) {
		if c.Kind == "v" {
			if _, has := c.Prop("gw"); has {
				verts = append(verts, c)
			}
		}
	}
	if len(verts) == 0 {
		return nil, nil
	}
	const hgap, vgap = 60.0, 70.0
	pos := map[string][2]float64{}
	switch algo {
	case "grid":
		cols := 1
		for cols*cols < len(verts) {
			cols++
		}
		x, y, rowH := 40.0, 40.0, 0.0
		for i, c := range verts {
			w, h := c.num("gw"), c.num("gh")
			pos[c.ID] = [2]float64{x, y}
			x += w + hgap
			if h > rowH {
				rowH = h
			}
			if (i+1)%cols == 0 {
				x, y, rowH = 40, y+rowH+vgap, 0
			}
		}
	default: // layered
		isV := map[string]*Cell{}
		for _, c := range verts {
			isV[c.ID] = c
		}
		out := map[string][]string{}
		indeg := map[string]int{}
		for _, c := range pg.Cells {
			if !c.Alive() || c.Kind != "e" {
				continue
			}
			s, t := c.srcID(), c.tgtID()
			if isV[s] != nil && isV[t] != nil && s != t {
				out[s] = append(out[s], t)
				indeg[t]++
			}
		}
		for k := range out {
			sort.Strings(out[k])
		}
		level := map[string]int{}
		var visit func(id string, stack map[string]bool, lv int)
		visit = func(id string, stack map[string]bool, lv int) {
			if stack[id] || level[id] >= lv && level[id] != 0 {
				return
			}
			if lv > level[id] {
				level[id] = lv
			}
			stack[id] = true
			for _, n := range out[id] {
				visit(n, stack, lv+1)
			}
			delete(stack, id)
		}
		for _, c := range verts {
			if indeg[c.ID] == 0 {
				visit(c.ID, map[string]bool{}, 0)
			}
		}
		for _, c := range verts { // vertices only in cycles
			if _, ok := level[c.ID]; !ok {
				visit(c.ID, map[string]bool{}, 0)
			}
		}
		rows := map[int][]*Cell{}
		maxLv := 0
		for _, c := range verts {
			rows[level[c.ID]] = append(rows[level[c.ID]], c)
			if level[c.ID] > maxLv {
				maxLv = level[c.ID]
			}
		}
		y := 40.0
		widest := 0.0
		widths := map[int]float64{}
		for lv := 0; lv <= maxLv; lv++ {
			w := 0.0
			for _, c := range rows[lv] {
				w += c.num("gw") + hgap
			}
			widths[lv] = w - hgap
			if widths[lv] > widest {
				widest = widths[lv]
			}
		}
		for lv := 0; lv <= maxLv; lv++ {
			x := 40 + (widest-widths[lv])/2
			rowH := 0.0
			for _, c := range rows[lv] {
				pos[c.ID] = [2]float64{x, y}
				x += c.num("gw") + hgap
				if h := c.num("gh"); h > rowH {
					rowH = h
				}
			}
			y += rowH + vgap
		}
	}
	var ops []Op
	for i, c := range verts {
		p, ok := pos[c.ID]
		if !ok {
			continue
		}
		x, y := p[0], p[1]
		ops = append(ops, Op{ID: idPrefix + "-" + Num(float64(i)), T: OpSetPos, Page: pageID, Cell: c.ID, X: &x, Y: &y})
	}
	return ops, nil
}

type layoutErr string

func (e layoutErr) Error() string { return string(e) }

var errPage = layoutErr("page not found")
