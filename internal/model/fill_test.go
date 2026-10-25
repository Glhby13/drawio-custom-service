package model

import "testing"

func TestFillOrdersMakesApiOpsValid(t *testing.T) {
	d, err := ImportXML(BlankXML)
	if err != nil {
		t.Fatal(err)
	}
	var page string
	for _, p := range d.SortedPages() {
		page = p.ID
	}
	ops := []Op{
		{ID: "a", T: OpAddCell, Page: page, Cell: "s1", Kind: "v", Parent: "1", Props: map[string]string{"v": "A", "gx": "1", "gy": "1", "gw": "10", "gh": "10"}},
		{ID: "b", T: OpAddCell, Page: page, Cell: "s2", Kind: "v", Parent: "1", Props: map[string]string{"v": "B", "gx": "1", "gy": "1", "gw": "10", "gh": "10"}},
		{ID: "c", T: OpAddPage, Page: "p2", Name: "Second"},
	}
	d.FillOrders(ops)
	if ops[0].Order == "" || ops[1].Order == "" || ops[0].Order >= ops[1].Order || ops[2].Order == "" {
		t.Fatalf("orders %q %q %q", ops[0].Order, ops[1].Order, ops[2].Order)
	}
	if _, oe := d.ApplyBatch(ops); oe != nil {
		t.Fatal(oe)
	}
}
