package model

import "encoding/json"

// StateCell / StatePage are the client-facing form of the document state: alive cells only, order keys and prop values
// included, no sequence numbers. The browser builds its model (web/model.js) from exactly this.
type StateCell struct {
	ID     string            `json:"id"`
	Kind   string            `json:"k"`
	Parent string            `json:"p"`
	Order  string            `json:"o"`
	Props  map[string]string `json:"props"`
}

// StatePage is one page of the client state.
type StatePage struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Order string      `json:"order"`
	Attrs string      `json:"attrs"`
	Cells []StateCell `json:"cells"`
}

// State returns the alive pages in order with their cells in parent-first, sibling order.
func (d *Doc) State() []StatePage {
	var out []StatePage
	for _, p := range d.SortedPages() {
		sp := StatePage{ID: p.ID, Name: p.Name.V, Order: p.Order.V, Attrs: p.Attrs.V, Cells: []StateCell{}}
		idx := p.childIndex()
		add := func(c *Cell) {
			props := map[string]string{}
			for k, r := range c.Props {
				if !r.Del {
					props[k] = r.V
				}
			}
			sp.Cells = append(sp.Cells, StateCell{ID: c.ID, Kind: c.Kind, Parent: c.Parent.V, Order: c.Order.V, Props: props})
		}
		var walk func(parent string)
		walk = func(parent string) {
			for _, c := range idx[parent] {
				add(c)
				walk(c.ID)
			}
		}
		if r := p.Cells["0"]; r != nil && r.Alive() {
			add(r)
			walk("0")
		}
		out = append(out, sp)
	}
	return out
}

// StateJSON is State marshalled.
func (d *Doc) StateJSON() []byte {
	b, _ := json.Marshal(d.State())
	return b
}
