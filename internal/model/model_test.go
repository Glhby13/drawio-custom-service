package model

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"
)

func sp(s string) *string   { return &s }
func fp(f float64) *float64 { return &f }

func TestBlankRoundTrip(t *testing.T) {
	d, err := ImportXML(BlankXML)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := ImportXML(d.ExportXML())
	if err != nil || d.Hash() != d2.Hash() {
		t.Fatalf("blank round trip: %v", err)
	}
}

// The corpus was exported by a real draw.io (spikes/spike-003-checksum): import must accept it and export must be a fixed point.
func TestRealDrawioCorpusRoundTrip(t *testing.T) {
	b, err := os.ReadFile("../../spikes/spike-003-checksum/results/corpus-final.json")
	if err != nil {
		t.Skip("corpus not present")
	}
	var corpus []struct{ Name, XML string }
	if err := json.Unmarshal(b, &corpus); err != nil {
		t.Fatal(err)
	}
	ok, skipped := 0, 0
	for _, c := range corpus {
		d, err := ImportXML(c.XML)
		if err != nil {
			if strings.Contains(err.Error(), "non-canonical") {
				skipped++
				continue
			}
			t.Errorf("%s: import: %v", c.Name, err)
			continue
		}
		x1 := d.ExportXML()
		d2, err := ImportXML(x1)
		if err != nil {
			t.Errorf("%s: re-import: %v", c.Name, err)
			continue
		}
		if d.Hash() != d2.Hash() {
			t.Errorf("%s: export is not a fixed point", c.Name)
			continue
		}
		if x2 := d2.ExportXML(); x1 != x2 {
			t.Errorf("%s: export not stable", c.Name)
			continue
		}
		ok++
	}
	t.Logf("corpus: %d round-tripped, %d skipped (non-canonical numbers), %d total", ok, skipped, len(corpus))
	if ok < len(corpus)-3 {
		t.Fatalf("too few round-tripped: %d/%d", ok, len(corpus))
	}
}

func TestImportRejectsHostileXML(t *testing.T) {
	for name, x := range map[string]string{
		"doctype": `<!DOCTYPE x [<!ENTITY a "b">]><mxfile/>`,
		"cdata":   `<mxfile><diagram id="p"><![CDATA[x]]></diagram></mxfile>`,
		"badnum":  `<mxfile><diagram id="p1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="a" vertex="1" parent="1"><mxGeometry x="5px" as="geometry"/></mxCell></root></mxGraphModel></diagram></mxfile>`,
		"orphan":  `<mxfile><diagram id="p1"><mxGraphModel><root><mxCell id="0"/><mxCell id="a" parent="zzz"/></root></mxGraphModel></diagram></mxfile>`,
		"dupcell": `<mxfile><diagram id="p1"><mxGraphModel><root><mxCell id="0"/><mxCell id="0"/></root></mxGraphModel></diagram></mxfile>`,
	} {
		if _, err := ImportXML(x); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ---- property test ---------------------------------------------------------------------------------------

type refCell struct {
	parent string
	x, y   float64
	alive  bool
}

type refModel map[string]map[string]*refCell // page -> cell

func (m refModel) desc(pg, id string) []string {
	var out []string
	seen := map[string]bool{id: true}
	for changed := true; changed; {
		changed = false
		for cid, c := range m[pg] {
			if !seen[cid] && c.parent != "" && seen[c.parent] {
				seen[cid], changed = true, true
				out = append(out, cid)
			}
		}
	}
	return out
}

func (m refModel) apply(o *Op) {
	pg := m[o.Page]
	switch o.T {
	case OpAddPage:
		m[o.Page] = map[string]*refCell{}
	case OpAddCell:
		x, y := parseNum(o.Props["gx"]), parseNum(o.Props["gy"])
		if p := pg[o.Parent]; p != nil {
			x, y = Round4(x+p.x), Round4(y+p.y)
		}
		pg[o.Cell] = &refCell{parent: o.Parent, x: x, y: y, alive: true}
	case OpSetPos:
		c := pg[o.Cell]
		dx, dy := *o.X-c.x, *o.Y-c.y
		c.x, c.y = *o.X, *o.Y
		for _, id := range m.desc(o.Page, o.Cell) {
			pg[id].x, pg[id].y = pg[id].x+dx, pg[id].y+dy
		}
	case OpReparent:
		pg[o.Cell].parent = o.Parent
	case OpDeleteCell:
		pg[o.Cell].alive = false
		for _, id := range m.desc(o.Page, o.Cell) {
			pg[id].alive = false
		}
	}
}

func invariants(t *testing.T, d *Doc, ctx string) {
	t.Helper()
	for _, p := range d.SortedPages() {
		if r := p.Cells["0"]; r == nil || !r.Alive() {
			t.Fatalf("%s: page %s lost root", ctx, p.ID)
		}
		layers := 0
		for id, c := range p.Cells {
			if !c.Alive() || id == "0" {
				continue
			}
			if c.Parent.V == "0" {
				layers++
			}
			if !ValidKey(c.Order.V) {
				t.Fatalf("%s: cell %s bad order %q", ctx, id, c.Order.V)
			}
			steps := 0
			for q := c.Parent.V; q != "0"; steps++ {
				pc := p.Cells[q]
				if pc == nil || !pc.Alive() || steps > len(p.Cells) {
					t.Fatalf("%s: cell %s has dead/missing/cyclic ancestor %q", ctx, id, q)
				}
				q = pc.Parent.V
			}
			for _, e := range []string{"src", "tgt"} {
				if v, ok := c.Prop(e); ok {
					if tc := p.Cells[v]; tc == nil || !tc.Alive() {
						t.Fatalf("%s: edge %s %s -> dead %q", ctx, id, e, v)
					}
				}
			}
		}
		if layers == 0 {
			t.Fatalf("%s: page %s has no layer", ctx, p.ID)
		}
	}
}

func pickCell(r *rand.Rand, d *Doc) (*Page, *Cell) {
	ps := d.SortedPages()
	if len(ps) == 0 {
		return nil, nil
	}
	p := ps[r.Intn(len(ps))]
	var cs []*Cell
	for id, c := range p.Cells {
		if c.Alive() && id != "0" {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return p, nil
	}
	sortCells(cs)
	return p, cs[r.Intn(len(cs))]
}

func genBatch(r *rand.Rand, d *Doc, n *int) []Op {
	id := func() string { *n++; return fmt.Sprintf("op%d", *n) }
	p, c := pickCell(r, d)
	if p == nil {
		return nil
	}
	pageCells := func() []*Cell {
		var cs []*Cell
		for _, x := range p.Cells {
			if x.Alive() && x.ID != "0" {
				cs = append(cs, x)
			}
		}
		sortCells(cs)
		return cs
	}
	keyIn := func(parent string) string {
		sib := p.Children(parent)
		i := r.Intn(len(sib) + 1)
		a, b := "", ""
		if i > 0 {
			a = sib[i-1].Order.V
		}
		if i < len(sib) {
			b = sib[i].Order.V
		}
		return Between(a, b)
	}
	switch r.Intn(14) {
	case 0, 1, 2:
		cs := pageCells()
		parent := "1"
		if len(cs) > 0 && r.Intn(2) == 0 {
			parent = cs[r.Intn(len(cs))].ID
		}
		cid := fmt.Sprintf("c%d", *n+1)
		return []Op{{ID: id(), T: OpAddCell, Page: p.ID, Cell: cid, Kind: "v", Parent: parent, Order: keyIn(parent),
			Props: map[string]string{"gx": Num(float64(r.Intn(400))), "gy": Num(float64(r.Intn(400))), "gw": "80", "gh": "40", "v": "n" + cid}}}
	case 3:
		if c != nil {
			return []Op{{ID: id(), T: OpSetPos, Page: p.ID, Cell: c.ID, X: fp(float64(r.Intn(800)) / 4), Y: fp(float64(r.Intn(800)) / 4)}}
		}
	case 4:
		if c != nil {
			k := []string{"v", "st:fillColor", "st:strokeColor", "va:label", "gw", "sn", "vt"}[r.Intn(7)]
			val := fmt.Sprintf("x%d", r.Intn(99))
			if k == "vt" {
				val = "UserObject"
			}
			if k == "gw" {
				val = Num(float64(10 + r.Intn(300)))
			}
			if r.Intn(5) == 0 {
				return []Op{{ID: id(), T: OpSetProp, Page: p.ID, Cell: c.ID, Key: k}}
			}
			return []Op{{ID: id(), T: OpSetProp, Page: p.ID, Cell: c.ID, Key: k, Val: sp(val)}}
		}
	case 5, 6:
		if c != nil {
			cs := pageCells()
			par := cs[r.Intn(len(cs))].ID
			if r.Intn(4) == 0 {
				par = "1"
			}
			return []Op{{ID: id(), T: OpReparent, Page: p.ID, Cell: c.ID, Parent: par, Order: keyIn(par)}}
		}
	case 7:
		if c != nil {
			return []Op{{ID: id(), T: OpReorder, Page: p.ID, Cell: c.ID, Order: keyIn(c.Parent.V)}}
		}
	case 8:
		if c != nil && r.Intn(2) == 0 {
			return []Op{{ID: id(), T: OpDeleteCell, Page: p.ID, Cell: c.ID}}
		}
	case 9:
		cs := pageCells()
		var es []*Cell
		for _, x := range cs {
			if x.Kind == "e" {
				es = append(es, x)
			}
		}
		if len(es) > 0 && len(cs) > 0 {
			return []Op{{ID: id(), T: OpConnect, Page: p.ID, Cell: es[r.Intn(len(es))].ID, End: []string{"src", "tgt"}[r.Intn(2)], Term: cs[r.Intn(len(cs))].ID}}
		}
		eid := fmt.Sprintf("e%d", *n+1)
		return []Op{{ID: id(), T: OpAddCell, Page: p.ID, Cell: eid, Kind: "e", Parent: "1", Order: keyIn("1"), Props: map[string]string{"grel": "1", "gx": "0", "gy": "0", "gw": "0", "gh": "0"}}}
	case 10:
		pid := fmt.Sprintf("pg%d", *n+1)
		ps := d.SortedPages()
		a, b := "", ""
		i := r.Intn(len(ps) + 1)
		if i > 0 {
			a = ps[i-1].Order.V
		}
		if i < len(ps) {
			b = ps[i].Order.V
		}
		return []Op{
			{ID: id(), T: OpAddPage, Page: pid, Name: "P" + pid, Order: Between(a, b)},
			{ID: id(), T: OpAddCell, Page: pid, Cell: "0", Kind: "-", Order: ""},
			{ID: id(), T: OpAddCell, Page: pid, Cell: "1", Kind: "-", Parent: "0", Order: "m"},
		}
	case 11:
		if len(d.SortedPages()) > 1 && r.Intn(3) == 0 {
			return []Op{{ID: id(), T: OpDeletePage, Page: p.ID}}
		}
	case 12:
		return []Op{{ID: id(), T: OpRenamePage, Page: p.ID, Name: fmt.Sprintf("n%d", r.Intn(50))}}
	case 13: // deliberately invalid
		return []Op{{ID: id(), T: OpSetProp, Page: p.ID, Cell: "nope", Key: "v", Val: sp("x")}}
	}
	return nil
}

func TestPropertyFuzz(t *testing.T) {
	runs := 400
	if os.Getenv("MODEL_FUZZ_RUNS") != "" {
		_, _ = fmt.Sscan(os.Getenv("MODEL_FUZZ_RUNS"), &runs)
	}
	for seed := 0; seed < runs; seed++ {
		r := rand.New(rand.NewSource(int64(seed)))
		d, _ := ImportXML(BlankXML)
		start := d.Clone()
		ref := refModel{}
		ref.apply(&Op{T: OpAddPage, Page: "p1"})
		ref["p1"]["0"] = &refCell{alive: true}
		ref["p1"]["1"] = &refCell{parent: "0", alive: true}
		var log []Op
		n := 0
		for step := 0; step < 120; step++ {
			batch := genBatch(r, d, &n)
			if len(batch) == 0 {
				continue
			}
			before := d.Canon()
			preDoc := d.Clone()
			out, oe := d.ApplyBatch(batch)
			if oe != nil {
				if d.Canon() != before {
					t.Fatalf("seed %d: rejected batch changed the document (%s)", seed, oe.Code)
				}
				continue
			}
			// reverting a batch right after it was applied restores the previous state exactly (page deletes are not revertable)
			hasPageDel := false
			for i := range out {
				if out[i].T == OpDeletePage {
					hasPageDel = true
				}
			}
			if !hasPageDel {
				rv := d.Clone()
				inv, _ := InverseOps(rv, out, "rv")
				applied, skipped := rv.ApplyBestEffort(inv)
				if len(skipped) > 0 || rv.Canon() != preDoc.Canon() {
					t.Fatalf("seed %d step %d: revert of %v did not restore the state (applied %d, skipped %v): %s", seed, step, opTypes(out), len(applied), skipped, firstDiff(preDoc.Canon(), rv.Canon()))
				}
			}
			for i := range out {
				log = append(log, out[i])
				ref.apply(&out[i])
				if os.Getenv("MODEL_TRACE") == fmt.Sprint(seed) {
					j, _ := json.Marshal(out[i])
					t.Logf("%s", j)
				}
			}
			invariants(t, d, fmt.Sprintf("seed %d step %d", seed, step))
		}
		// replay from the initial state reproduces the final state; applying the log twice changes nothing
		rep := start.Clone()
		for i := range log {
			rep.Apply(&log[i])
		}
		if rep.Hash() != d.Hash() {
			t.Fatalf("seed %d: replay mismatch", seed)
		}
		for i := range log {
			rep.Apply(&log[i])
		}
		if rep.Hash() != d.Hash() {
			t.Fatalf("seed %d: applying the log twice changed the state (idempotency)", seed)
		}
		// resolved ops survive a JSON round trip (they are what the log stores)
		j, _ := json.Marshal(log)
		var back []Op
		if err := json.Unmarshal(j, &back); err != nil {
			t.Fatal(err)
		}
		rep2 := start.Clone()
		for i := range back {
			rep2.Apply(&back[i])
		}
		if rep2.Hash() != d.Hash() {
			t.Fatalf("seed %d: JSON round trip of the log changed the replay", seed)
		}
		// independent intent oracle: absolute positions
		for pid, cells := range ref {
			pg := d.Pages[pid]
			if pg == nil || pg.Dead != 0 {
				continue
			}
			for id, rc := range cells {
				c := pg.Cells[id]
				if c == nil || !rc.alive || !c.Alive() || id == "0" || id == "1" || isRel(c) { // relative geometry (edge labels) has no absolute position
					if c != nil && c.Alive() != rc.alive && id != "0" {
						t.Fatalf("seed %d: alive mismatch %s/%s", seed, pid, id)
					}
					continue
				}
				ax, ay := pg.Abs(c)
				if math.Abs(ax-rc.x) > 0.01 || math.Abs(ay-rc.y) > 0.01 {
					t.Fatalf("seed %d: WRONG INTENT %s/%s abs (%v,%v) want (%v,%v)", seed, pid, id, ax, ay, rc.x, rc.y)
				}
			}
		}
		// the final document exports and re-imports to the same state
		d2, err := ImportXML(d.ExportXML())
		if err != nil {
			t.Fatalf("seed %d: export/import: %v", seed, err)
		}
		a, b := respread(d), respread(d2)
		if a.Hash() != b.Hash() {
			t.Fatalf("seed %d: export/import changed the state: %s", seed, firstDiff(a.Canon(), b.Canon()))
		}
	}
}

// respread replaces every order key by the well-spaced keys an import assigns, so documents can be compared by structure
// (export keeps sibling ORDER, not the exact key strings).
func respread(d *Doc) *Doc {
	n := d.Clone()
	ps := n.SortedPages()
	ks := Spread(len(ps))
	for i, p := range ps {
		p.Order.V = ks[i]
		idx := p.childIndex()
		for _, list := range idx {
			k2 := Spread(len(list))
			for j, c := range list {
				c.Order.V = k2[j]
			}
		}
	}
	return n
}

func firstDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) || i < len(lb); i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y {
			return fmt.Sprintf("line %d\n  before: %s\n  after : %s", i, x, y)
		}
	}
	return "identical"
}

func opTypes(ops []Op) []string {
	var t []string
	for _, o := range ops {
		t = append(t, o.T)
	}
	return t
}

// TestWriteWebVectors writes conformance vectors for the browser model (webtest/model.test.mjs):
// WRITE_VECTORS=/path/vectors.json go test ./internal/model -run WriteWebVectors
func TestWriteWebVectors(t *testing.T) {
	path := os.Getenv("WRITE_VECTORS")
	if path == "" {
		t.Skip("WRITE_VECTORS not set")
	}
	type step struct {
		Authored []Op   `json:"authored"`
		Resolved []Op   `json:"resolved"`
		Rejected string `json:"rejected,omitempty"`
		Hash     string `json:"hash"`
	}
	type vec struct {
		Seed  int    `json:"seed"`
		Start any    `json:"start"`
		Steps []step `json:"steps"`
		Canon string `json:"canon"`
	}
	var vs []vec
	for seed := 0; seed < 40; seed++ {
		r := rand.New(rand.NewSource(int64(1000 + seed)))
		d, _ := ImportXML(BlankXML)
		v := vec{Seed: seed, Start: map[string]any{"state": d.State(), "seq": d.Seq}}
		n := 0
		for i := 0; i < 70; i++ {
			batch := genBatch(r, d, &n)
			if len(batch) == 0 {
				continue
			}
			out, oe := d.ApplyBatch(append([]Op(nil), batch...))
			st := step{Authored: batch, Hash: d.Hash()}
			if oe != nil {
				st.Rejected = oe.Code
			} else {
				st.Resolved = out
			}
			v.Steps = append(v.Steps, st)
		}
		v.Canon = d.Canon()
		vs = append(vs, v)
	}
	b, _ := json.Marshal(vs)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiffOpsFromBrowserAreAcceptedByGo replays the same histories as TestWriteWebVectors and applies the ops that the
// browser's diff() produced for each transition (webtest/diffops.json, written by webtest/bridge.test.mjs): the Go
// validator must accept every batch and reach the same structure.
func TestDiffOpsFromBrowserAreAcceptedByGo(t *testing.T) {
	raw, err := os.ReadFile("../../webtest/diffops.json")
	if err != nil {
		t.Skip("diffops.json not generated (run node webtest/bridge.test.mjs)")
	}
	var diffops map[string]map[string][]Op
	if err := json.Unmarshal(raw, &diffops); err != nil {
		t.Fatal(err)
	}
	checked, empty := 0, 0
	for seed := 0; seed < 40; seed++ {
		r := rand.New(rand.NewSource(int64(1000 + seed)))
		d, _ := ImportXML(BlankXML)
		n, stepIdx := 0, -1
		for i := 0; i < 70; i++ {
			batch := genBatch(r, d, &n)
			if len(batch) == 0 {
				continue
			}
			stepIdx++
			before := d.Clone()
			_, oe := d.ApplyBatch(append([]Op(nil), batch...))
			if oe != nil {
				continue
			}
			ops := diffops[fmt.Sprint(seed)][fmt.Sprint(stepIdx)]
			w := before.Clone()
			if len(ops) > 0 {
				if _, oe := w.ApplyBatch(ops); oe != nil {
					t.Fatalf("seed %d step %d: Go rejected the browser diff: %v (ops %v)", seed, stepIdx, oe, opTypes(ops))
				}
			} else {
				empty++
			}
			if respread(w).Hash() != respread(d).Hash() {
				t.Fatalf("seed %d step %d: browser diff reaches another state: %s", seed, stepIdx, firstDiff(respread(d).Canon(), respread(w).Canon()))
			}
			checked++
		}
	}
	t.Logf("%d browser diffs accepted by Go (%d were empty)", checked, empty)
	if checked < 1000 {
		t.Fatalf("only %d checked", checked)
	}
}
