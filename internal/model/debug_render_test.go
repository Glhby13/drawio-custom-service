package model

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestRenderDebug writes an SVG of the laid-out architecture when DRAWIO_DEBUG_SVG is set (a developer aid, skipped in CI).
func TestRenderDebug(t *testing.T) {
	out := os.Getenv("DRAWIO_DEBUG_SVG")
	if out == "" {
		t.Skip("set DRAWIO_DEBUG_SVG=<dir> to render")
	}
	for _, dir := range []string{"TB", "LR"} {
		spec := archSpec()
		spec.Direction = dir
		d, lr, page := buildAndLint(t, spec)
		pg := d.Pages[page]
		var b strings.Builder
		b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="2200" height="1500" font-family="Helvetica" font-size="12"><rect width="100%" height="100%" fill="#fff"/><defs><marker id="a" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0,0L10,5L0,10z" fill="#555"/></marker></defs>`)
		shapes := map[string]*lintShape{}
		for _, c := range sortedCells(pg) {
			if c.Kind != "v" {
				continue
			}
			if _, ok := c.Prop("gw"); !ok {
				continue
			}
			x, y := pg.Abs(c)
			sh := &lintShape{c: c, box: rect{x, y, c.num("gw"), c.num("gh")}}
			shapes[c.ID] = sh
			v, _ := c.Prop("v")
			st, _ := c.Prop("st:container")
			fill, op := "#dae8fc", "1"
			if st == "1" {
				fill, op = "#eeeeee", ".5"
			}
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="8" fill="%s" fill-opacity="%s" stroke="#678" stroke-dasharray="%s"/>`, x, y, sh.box.w, sh.box.h, fill, op, map[bool]string{true: "6 4", false: ""}[st == "1"])
			ty := y + sh.box.h/2 + 4
			if st == "1" {
				ty = y + 20
			}
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="%s">%s</text>`, x+map[bool]float64{true: 12, false: sh.box.w / 2}[st == "1"], ty, map[bool]string{true: "start", false: "middle"}[st == "1"], strings.SplitN(labelText(v), "\n", 2)[0])
		}
		for _, c := range sortedCells(pg) {
			if c.Kind != "e" {
				continue
			}
			pts := edgePolyline(c, shapes[c.srcID()], shapes[c.tgtID()])
			var p []string
			for _, q := range pts {
				p = append(p, fmt.Sprintf("%.1f,%.1f", q[0], q[1]))
			}
			fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="#c33" stroke-width="1.5" marker-end="url(#a)"/>`, strings.Join(p, " "))
		}
		b.WriteString("</svg>")
		if err := os.WriteFile(out+"/arch-"+dir+".svg", []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s crossings=%d", dir, lr.Crossings)
	}
}
