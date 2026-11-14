// Author / Agent: Codex
package model

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// Each parallel worker owns its document; resolved logs are replayed independently.
func TestMassiveProperties(t *testing.T) {
	for _, seed := range []int64{1, 7, 42, 271828, 8675309, 0xdeadbeef} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewSource(seed))
			d, err := ImportXML(BlankXML)
			if err != nil {
				t.Fatal(err)
			}
			start := d.Clone()
			var log []Op
			n := 0
			apply := func(o Op) []Op { n++; o.ID = fmt.Sprintf("massive-%d", n); return mustBatch(t, d, &log, []Op{o}) }
			add := func(id, kind, parent, style string) {
				var props map[string]string
				switch kind {
				case "v":
					props = map[string]string{"gx": "10", "gy": "20", "gw": "100", "gh": "60", "v": id}
					if style != "" {
						props["st:shape"] = style
					}
				case "e":
					props = map[string]string{"grel": "1", "gx": "0", "gy": "0", "gw": "0", "gh": "0", "v": id}
				}
				order := "m"
				if id == "0" {
					order = ""
				}
				apply(Op{T: OpAddCell, Page: "p2", Cell: id, Kind: kind, Parent: parent, Order: order, Props: props})
			}
			var midpoint *Doc
			mid := 0
			for cycle := 0; cycle < 20; cycle++ {
				apply(Op{T: OpAddPage, Page: "p2", Name: "Page", Order: "t"})
				add("0", "-", "", "")
				add("1", "-", "0", "")
				add("a", "v", "1", "swimlane")
				add("b", "v", "a", "table")
				add("c", "v", "b", "rectangle")
				add(fmt.Sprintf("new%d", cycle), "v", "1", "rectangle")
				add("e", "e", "1", "")
				for _, code := range []struct {
					o    Op
					want string
				}{
					{Op{ID: "dup", T: OpAddPage, Page: "p2"}, "duplicate_page"},
					{Op{ID: "dup", T: OpAddCell, Page: "p2", Cell: "a"}, "duplicate_cell"},
					{Op{ID: "bad", T: OpAddCell, Page: "p2", Cell: "invalid", Kind: "?"}, "bad_kind"},
				} {
					if got := d.Validate(&code.o); got != code.want {
						t.Fatalf("Validate=%s want %s", got, code.want)
					}
				}
				for j := 0; j < 18; j++ {
					x, y := float64(rng.Intn(1000)), float64(rng.Intn(1000))
					ops := []Op{
						{T: OpSetProp, Cell: "c", Key: "v", Val: sp(fmt.Sprint(rng.Int()))},
						{T: OpSetPos, Cell: "c", X: &x, Y: &y},
						{T: OpConnect, Cell: "e", End: "src", Term: "c"},
						{T: OpReparent, Cell: "c", Parent: "b", Order: "t"},
						{T: OpReorder, Cell: "c", Order: "g"},
						{T: OpRenamePage, Name: fmt.Sprint(rng.Int())},
						{T: OpReorderPage, Order: "t"},
						{T: OpSetAttrs, Attrs: ""},
					}
					o := ops[(j+cycle)%len(ops)]
					o.Page = "p2"
					apply(o)
				}
				before := d.Clone()
				deleted := apply(Op{T: OpDeleteCell, Page: "p2", Cell: "a"})
				if d.Pages["p2"].Cells["c"].Alive() {
					t.Fatal("cascade left descendant alive")
				}
				if v, ok := d.Pages["p2"].Cells["e"].Prop("src"); ok && v != "" {
					t.Fatal("terminal not cleared")
				}
				inv, _ := InverseOps(d, deleted, fmt.Sprintf("undo%d", cycle))
				applied, skipped := d.ApplyBestEffort(inv)
				if len(skipped) > 0 {
					t.Fatal(skipped)
				}
				log = append(log, applied...)
				if d.Hash() != before.Hash() {
					t.Fatal("inverse failed to restore canonical state")
				}
				apply(Op{T: OpDeleteCell, Page: "p2", Cell: "a"})
				add("a", "v", "1", "rectangle")
				add("b", "v", "a", "table")
				add("c", "v", "b", "ellipse")
				if cycle == 9 {
					midpoint = d.Clone()
					mid = len(log)
				}
				apply(Op{T: OpDeletePage, Page: "p2"})
			}
			// Leave a revived page and genuinely new cell in the final state.
			apply(Op{T: OpAddPage, Page: "p2", Name: "Final", Order: "t"})
			add("0", "-", "", "")
			add("1", "-", "0", "")
			add("final", "v", "1", "rectangle")
			t.Run("jsonXML", func(t *testing.T) {
				raw, err := json.Marshal(log)
				if err != nil {
					t.Fatal(err)
				}
				var back []Op
				if err = json.Unmarshal(raw, &back); err != nil {
					t.Fatal(err)
				}
				rep := start.Clone()
				for i := range back {
					rep.Apply(&back[i])
				}
				if rep.Hash() != d.Hash() {
					t.Fatal("JSON replay mismatch")
				}
				xml, err := ImportXML(d.ExportXML())
				if err != nil {
					t.Fatal(err)
				}
				if respread(xml).Hash() != respread(d).Hash() {
					t.Fatal("XML canonical parity mismatch")
				}
			})
			t.Run("fullReplayAndXML", func(t *testing.T) { assertReplayAndXML(t, start, d, log) })
			for pass := 0; pass < 2; pass++ {
				for i := mid; i < len(log); i++ {
					midpoint.Apply(&log[i])
				}
				if midpoint.Hash() != d.Hash() {
					t.Fatalf("midpoint tail pass %d mismatch", pass)
				}
			}
			t.Logf("seed=%d resolved operations=%d", seed, len(log))
		})
	}
}
