package model

import (
	"encoding/json"
	"testing"
)

func mustBatch(t *testing.T, d *Doc, log *[]Op, ops []Op) []Op {
	t.Helper()
	out, oe := d.ApplyBatch(ops)
	if oe != nil {
		t.Fatalf("ApplyBatch failed: %v", oe)
	}
	*log = append(*log, out...)
	return out
}

func assertReplayAndXML(t *testing.T, start, d *Doc, log []Op) {
	t.Helper()
	rep := start.Clone()
	for i := range log {
		rep.Apply(&log[i])
	}
	if rep.Hash() != d.Hash() {
		t.Fatalf("replay mismatch:\nwant:\n%s\ngot:\n%s", d.Canon(), rep.Canon())
	}
	for i := range log {
		rep.Apply(&log[i])
	}
	if rep.Hash() != d.Hash() {
		t.Fatalf("idempotency (2x replay) mismatch:\nwant:\n%s\ngot:\n%s", d.Canon(), rep.Canon())
	}
	raw, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	var back []Op
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	rep2 := start.Clone()
	for i := range back {
		rep2.Apply(&back[i])
	}
	if rep2.Hash() != d.Hash() {
		t.Fatalf("JSON round-trip replay mismatch")
	}
	d2, err := ImportXML(d.ExportXML())
	if err != nil {
		t.Fatalf("ExportXML/ImportXML failed: %v", err)
	}
	if respread(d2).Hash() != respread(d).Hash() {
		t.Fatalf("XML round-trip mismatch:\nwant:\n%s\ngot:\n%s", respread(d).Canon(), respread(d2).Canon())
	}
}

func TestAddCellRevivesTombstone(t *testing.T) {
	d, err := ImportXML(BlankXML)
	if err != nil {
		t.Fatal(err)
	}
	start := d.Clone()
	var log []Op

	// Create container c1 with child c2, target c3, and edge e1.
	mustBatch(t, d, &log, []Op{
		{ID: "op1", T: OpAddCell, Page: "p1", Cell: "c1", Kind: "v", Parent: "1", Order: "g",
			Props: map[string]string{"gx": "10", "gy": "20", "gw": "200", "gh": "150", "v": "Container", "st:fillColor": "#ffffff"}},
		{ID: "op2", T: OpAddCell, Page: "p1", Cell: "c2", Kind: "v", Parent: "c1", Order: "g",
			Props: map[string]string{"gx": "10", "gy": "30", "gw": "80", "gh": "40", "v": "Child"}},
		{ID: "op3", T: OpAddCell, Page: "p1", Cell: "c3", Kind: "v", Parent: "1", Order: "m",
			Props: map[string]string{"gx": "300", "gy": "20", "gw": "80", "gh": "40", "v": "Target"}},
		{ID: "op4", T: OpAddCell, Page: "p1", Cell: "e1", Kind: "e", Parent: "1", Order: "t",
			Props: map[string]string{"grel": "1", "gx": "0", "gy": "0", "gw": "0", "gh": "0", "src": "c2", "tgt": "c3", "v": "link"}},
	})

	// Alive duplicate and kind mismatch must still fail.
	if _, oe := d.ApplyBatch([]Op{{ID: "dup1", T: OpAddCell, Page: "p1", Cell: "c1", Kind: "v", Parent: "1", Order: "z"}}); oe == nil || oe.Code != "duplicate_cell" {
		t.Fatalf("expected duplicate_cell on alive cell, got %v", oe)
	}

	// Delete c1 (cascades to c2, clears e1.src) and delete e1.
	mustBatch(t, d, &log, []Op{
		{ID: "op5", T: OpDeleteCell, Page: "p1", Cell: "c1"},
		{ID: "op6", T: OpDeleteCell, Page: "p1", Cell: "e1"},
	})

	// Changing kind on a tombstoned cell must be rejected.
	if _, oe := d.ApplyBatch([]Op{{ID: "badk", T: OpAddCell, Page: "p1", Cell: "c1", Kind: "e", Parent: "1", Order: "g"}}); oe == nil || oe.Code != "bad_kind" {
		t.Fatalf("expected bad_kind when changing kind of tombstoned cell, got %v", oe)
	}

	// Undo delete via addCell (with modified/dropped props like st:fillColor removed and st:strokeColor added).
	preRevive := d.Clone()
	reviveBatch := mustBatch(t, d, &log, []Op{
		{ID: "op7", T: OpAddCell, Page: "p1", Cell: "c1", Kind: "v", Parent: "1", Order: "g",
			Props: map[string]string{"gx": "10", "gy": "20", "gw": "200", "gh": "150", "v": "ContainerRevived", "st:strokeColor": "#000000"}},
		{ID: "op8", T: OpAddCell, Page: "p1", Cell: "c2", Kind: "v", Parent: "c1", Order: "g",
			Props: map[string]string{"gx": "10", "gy": "30", "gw": "80", "gh": "40", "v": "Child"}},
		{ID: "op9", T: OpAddCell, Page: "p1", Cell: "e1", Kind: "e", Parent: "1", Order: "t",
			Props: map[string]string{"grel": "1", "gx": "0", "gy": "0", "gw": "0", "gh": "0", "src": "c2", "tgt": "c3", "v": "link"}},
	})

	if _, hasOld := d.Pages["p1"].Cells["c1"].Prop("st:fillColor"); hasOld {
		t.Fatalf("revived cell kept stale prop st:fillColor")
	}
	if v, ok := d.Pages["p1"].Cells["c1"].Prop("v"); !ok || v != "ContainerRevived" {
		t.Fatalf("revived cell label = %q, %v", v, ok)
	}

	// Server-side revert of the revival batch must restore preRevive.
	rv := d.Clone()
	inv, _ := InverseOps(rv, reviveBatch, "rv1")
	if _, skipped := rv.ApplyBestEffort(inv); len(skipped) > 0 || rv.Canon() != preRevive.Canon() {
		t.Fatalf("revert of revival failed: skipped=%v\n%s", skipped, firstDiff(preRevive.Canon(), rv.Canon()))
	}

	// Single-op re-apply of revive op and earlier deleteCell must be no-ops.
	snapBeforeDup := d.Hash()
	d.Apply(&reviveBatch[0])
	d.Apply(&log[4]) // earlier deleteCell op5 on c1
	if d.Hash() != snapBeforeDup {
		t.Fatalf("re-applying single revive or stale delete op mutated state")
	}

	// Mutate revived cell, delete it again, and keep it dead to test s < c.Dead on replay.
	mustBatch(t, d, &log, []Op{
		{ID: "op10", T: OpSetProp, Page: "p1", Cell: "c1", Key: "v", Val: sp("Updated")},
		{ID: "op11", T: OpDeleteCell, Page: "p1", Cell: "c1"},
	})
	assertReplayAndXML(t, start, d, log)

	// Re-revive one more time so final state has c1 alive as well.
	mustBatch(t, d, &log, []Op{
		{ID: "op12", T: OpAddCell, Page: "p1", Cell: "c1", Kind: "v", Parent: "1", Order: "g",
			Props: map[string]string{"gx": "10", "gy": "20", "gw": "200", "gh": "150", "v": "BackAgain"}},
	})
	assertReplayAndXML(t, start, d, log)
}

func TestAddPageRevivesTombstone(t *testing.T) {
	d, err := ImportXML(BlankXML)
	if err != nil {
		t.Fatal(err)
	}
	start := d.Clone()
	var log []Op

	mustBatch(t, d, &log, []Op{
		{ID: "p-add1", T: OpAddPage, Page: "p2", Name: "Page-2", Order: "t"},
		{ID: "p-c0", T: OpAddCell, Page: "p2", Cell: "0", Kind: "-", Order: ""},
		{ID: "p-c1", T: OpAddCell, Page: "p2", Cell: "1", Kind: "-", Parent: "0", Order: "m"},
		{ID: "p-v1", T: OpAddCell, Page: "p2", Cell: "v1", Kind: "v", Parent: "1", Order: "m",
			Props: map[string]string{"gx": "20", "gy": "20", "gw": "100", "gh": "50", "v": "Keep"}},
		{ID: "p-v2", T: OpAddCell, Page: "p2", Cell: "v2", Kind: "v", Parent: "1", Order: "t",
			Props: map[string]string{"gx": "150", "gy": "20", "gw": "100", "gh": "50", "v": "OmittedOnRevive"}},
	})

	if _, oe := d.ApplyBatch([]Op{{ID: "dup-pg", T: OpAddPage, Page: "p2", Name: "Dup", Order: "z"}}); oe == nil || oe.Code != "duplicate_page" {
		t.Fatalf("expected duplicate_page on alive page, got %v", oe)
	}

	mustBatch(t, d, &log, []Op{
		{ID: "p-del1", T: OpDeletePage, Page: "p2"},
	})

	// Undo deletePage via addPage + addCell (without v2).
	revivePg := mustBatch(t, d, &log, []Op{
		{ID: "p-add2", T: OpAddPage, Page: "p2", Name: "Page-2-Restored", Order: "t"},
		{ID: "p-c0-2", T: OpAddCell, Page: "p2", Cell: "0", Kind: "-", Order: ""},
		{ID: "p-c1-2", T: OpAddCell, Page: "p2", Cell: "1", Kind: "-", Parent: "0", Order: "m"},
		{ID: "p-v1-2", T: OpAddCell, Page: "p2", Cell: "v1", Kind: "v", Parent: "1", Order: "m",
			Props: map[string]string{"gx": "20", "gy": "20", "gw": "100", "gh": "50", "v": "Keep"}},
	})

	if d.Pages["p2"].Cells["v2"].Alive() {
		t.Fatalf("v2 was not re-added after page revival so it should remain tombstoned")
	}
	if !d.Pages["p2"].Cells["v1"].Alive() {
		t.Fatalf("v1 should be alive after page revival")
	}

	// Single-op re-apply of addPage revive must be a no-op.
	h := d.Hash()
	d.Apply(&revivePg[0])
	if d.Hash() != h {
		t.Fatalf("re-applying single page revive op mutated state")
	}

	assertReplayAndXML(t, start, d, log)

	// Add genuinely new cells after page revival, plus another cell delete/revive cycle, and verify 2x full-log replay.
	mustBatch(t, d, &log, []Op{
		{ID: "p-v3", T: OpAddCell, Page: "p2", Cell: "v3", Kind: "v", Parent: "1", Order: "z",
			Props: map[string]string{"gx": "280", "gy": "20", "gw": "100", "gh": "50", "v": "AddedAfterRevive"}},
	})
	assertReplayAndXML(t, start, d, log)
	if !d.Pages["p2"].Cells["v3"].Alive() {
		t.Fatalf("v3 added after page revival should remain alive")
	}

	// Single-op re-apply of earlier deletePage/deleteCell after revival must be no-ops.
	hBeforeStale := d.Hash()
	d.Apply(&log[5]) // earlier p-del1
	if d.Hash() != hBeforeStale {
		t.Fatalf("re-applying stale deletePage after revival mutated state")
	}

	// Delete page again and ensure replay with s < p.Dead still holds.
	mustBatch(t, d, &log, []Op{
		{ID: "p-del2", T: OpDeletePage, Page: "p2"},
	})
	assertReplayAndXML(t, start, d, log)
}

func TestReviveAndRestoreGuards(t *testing.T) {
	d, err := ImportXML(BlankXML)
	if err != nil {
		t.Fatal(err)
	}
	var log []Op
	mustBatch(t, d, &log, []Op{
		{ID: "g1", T: OpAddCell, Page: "p1", Cell: "v1", Kind: "v", Parent: "1", Order: "g",
			Props: map[string]string{"gx": "10", "gy": "10", "gw": "80", "gh": "40", "v": "Alive"}},
	})

	// OpRestore must reject missing cell and already-alive cell.
	if _, oe := d.ApplyBatch([]Op{{ID: "r-miss", T: OpRestore, Page: "p1", Cells: []CellSnap{{ID: "missing", Kind: "v", Parent: "1", Order: "m"}}}}); oe == nil || oe.Code != "cell_not_deleted" {
		t.Fatalf("expected cell_not_deleted for missing cell, got %v", oe)
	}
	if _, oe := d.ApplyBatch([]Op{{ID: "r-alive", T: OpRestore, Page: "p1", Cells: []CellSnap{{ID: "v1", Kind: "v", Parent: "1", Order: "g"}}}}); oe == nil || oe.Code != "cell_not_deleted" {
		t.Fatalf("expected cell_not_deleted for alive cell, got %v", oe)
	}

	// Delete v1 at seq 2, then test Apply guards for s < Dead and stale deleteCell.
	delOps := mustBatch(t, d, &log, []Op{{ID: "g2", T: OpDeleteCell, Page: "p1", Cell: "v1"}})
	delSeq := delOps[0].Seq
	snapDead := d.Hash()

	// Out-of-order Apply of addCell / restoreCells with s < c.Dead or missing cell must be no-ops.
	d.Apply(&Op{ID: "old-add", Seq: delSeq - 1, T: OpAddCell, Page: "p1", Cell: "v1", Kind: "v", Parent: "1", Order: "g"})
	d.Apply(&Op{ID: "old-rst", Seq: delSeq - 1, T: OpRestore, Page: "p1", Cells: []CellSnap{{ID: "v1", Kind: "v", Parent: "1", Order: "g"}}})
	d.Apply(&Op{ID: "nil-rst", Seq: delSeq + 1, T: OpRestore, Page: "p1", Cells: []CellSnap{{ID: "no-such", Kind: "v", Parent: "1", Order: "g"}}})
	if d.Hash() != snapDead {
		t.Fatalf("stale add/restore or missing-cell restore mutated tombstoned state")
	}

	// Revive v1 via OpAddCell at higher seq, then ensure stale OpDeleteCell (s < Parent.Seq) is a no-op.
	revOps := mustBatch(t, d, &log, []Op{{ID: "g3", T: OpAddCell, Page: "p1", Cell: "v1", Kind: "v", Parent: "1", Order: "g",
		Props: map[string]string{"gx": "10", "gy": "10", "gw": "80", "gh": "40", "v": "Revived"}}})
	revSeq := revOps[0].Seq
	snapRevived := d.Hash()
	d.Apply(&Op{ID: "stale-del", Seq: revSeq - 1, T: OpDeleteCell, Page: "p1", Cell: "v1", Res: &Resolved{Del: []string{"v1"}}})
	if d.Hash() != snapRevived {
		t.Fatalf("stale deleteCell with s < Parent.Seq killed revived cell")
	}

	// Page revival: an already-tombstoned page rejects stale OpAddPage (s < Dead), and a cell with Parent.Seq >= s stays alive on replay.
	mustBatch(t, d, &log, []Op{
		{ID: "pg-a", T: OpAddPage, Page: "p2", Name: "P2", Order: "t"},
		{ID: "pg-0", T: OpAddCell, Page: "p2", Cell: "0", Kind: "v", Parent: "", Order: ""},
	})
	pgDel := mustBatch(t, d, &log, []Op{{ID: "pg-d", T: OpDeletePage, Page: "p2"}})
	pgSnap := d.Hash()
	d.Apply(&Op{ID: "pg-old", Seq: pgDel[0].Seq - 1, T: OpAddPage, Page: "p2", Name: "Old", Order: "t"})
	if d.Hash() != pgSnap {
		t.Fatalf("stale OpAddPage with s < p.Dead revived deleted page")
	}
}
