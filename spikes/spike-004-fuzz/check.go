// DISPOSABLE SPIKE — NOT PRODUCTION CODE. Property checks after quiescence. The reference model below is a second,
// independent implementation of the intended semantics (absolute coordinates, parent pointers, no registers).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

type rc struct {
	page, parent, order string
	kind                byte
	alive               bool
	x, y                int
	props               map[string]string
	src, tgt            string
	lastActor           map[string]int
	lastSeq             map[string]int64
}

type refPage struct {
	alive       bool
	name, order string
}

type refDoc struct {
	cells map[string]*rc
	pages map[string]*refPage
}

func foldRef(log []*Op, metrics map[string]int) *refDoc {
	r := &refDoc{cells: map[string]*rc{}, pages: map[string]*refPage{}}
	desc := func(id string) []string { // transitive children by reference parent pointers
		var out []string
		seen := map[string]bool{id: true}
		for changed := true; changed; {
			changed = false
			for cid, c := range r.cells {
				if !seen[cid] && c.parent != "" && seen[c.parent] {
					seen[cid] = true
					out = append(out, cid)
					changed = true
				}
			}
		}
		return out
	}
	for _, o := range log {
		switch o.T {
		case "addPage":
			r.pages[o.Page] = &refPage{true, o.Name, o.Key}
		case "renamePage":
			r.pages[o.Page].name = o.Name
		case "reorderPage":
			r.pages[o.Page].order = o.Key
		case "deletePage":
			r.pages[o.Page].alive = false
			for _, c := range r.cells {
				if c.page == o.Page {
					c.alive = false
				}
			}
		case "addCell":
			r.cells[o.Cell] = &rc{page: o.Page, parent: o.Par, order: o.Key, kind: o.Kind, alive: true, x: o.IX, y: o.IY,
				props: map[string]string{"value": o.Val}, lastActor: map[string]int{}, lastSeq: map[string]int64{}}
		case "setProp":
			c := r.cells[o.Cell]
			if la, ok := c.lastActor[o.Key]; ok && la != o.Actor && o.Base < c.lastSeq[o.Key] {
				metrics["concurrent_overwrite"]++
			}
			c.props[o.Key], c.lastActor[o.Key], c.lastSeq[o.Key] = o.Val, o.Actor, o.Seq
		case "setPos":
			c := r.cells[o.Cell]
			dx, dy := o.IX-c.x, o.IY-c.y
			c.x, c.y = o.IX, o.IY
			for _, id := range desc(o.Cell) {
				r.cells[id].x += dx
				r.cells[id].y += dy
			}
		case "reparent":
			c := r.cells[o.Cell]
			c.parent, c.order = o.Par, o.Key // absolute position is preserved by definition
		case "reorder":
			r.cells[o.Cell].order = o.Key
		case "connect":
			c := r.cells[o.Cell]
			if o.Term == "src" {
				c.src = o.TermV
			} else {
				c.tgt = o.TermV
			}
		case "deleteCell":
			dead := map[string]bool{o.Cell: true}
			r.cells[o.Cell].alive = false
			for _, id := range desc(o.Cell) {
				dead[id] = true
				r.cells[id].alive = false
			}
			for _, c := range r.cells {
				if c.alive && dead[c.src] {
					c.src = ""
				}
				if c.alive && dead[c.tgt] {
					c.tgt = ""
				}
			}
		}
	}
	return r
}

func fold(log []*Op, from *Doc) *Doc {
	d := from
	if d == nil {
		d = NewDoc()
	}
	for _, o := range log {
		d.Apply(o)
	}
	return d
}

func invariants(d *Doc, who string, fail func(string, ...any)) {
	for id, c := range d.Cells {
		if !c.alive() {
			continue
		}
		if d.Pages[c.Page].Dead != 0 {
			fail("%s: alive cell %s in dead page %s", who, id, c.Page)
		}
		if !ValidKey(c.Order.V) {
			fail("%s: cell %s has invalid order key %q", who, id, c.Order.V)
		}
		steps := 0
		for p := c.Parent.V; p != ""; steps++ {
			pc, ok := d.Cells[p]
			if !ok || !pc.alive() {
				fail("%s: cell %s has missing/dead ancestor %q", who, id, p)
				break
			}
			if pc.Page != c.Page {
				fail("%s: cell %s parent %s is on another page", who, id, p)
				break
			}
			if steps > len(d.Cells) {
				fail("%s: CYCLE through cell %s", who, id)
				break
			}
			p = pc.Parent.V
		}
		if c.Kind == 'e' {
			for _, t := range []string{"src", "tgt"} {
				if r, ok := c.Props[t]; ok && r.V != "" {
					tc, ok := d.Cells[r.V]
					if !ok || !tc.alive() {
						fail("%s: edge %s terminal %s -> dead/missing %q", who, id, t, r.V)
					}
				}
			}
		}
	}
	for _, p := range d.SortedPages() {
		if !ValidKey(p.Order.V) {
			fail("%s: page %s invalid order key", who, p.ID)
		}
	}
}

func (s *sim) check() *Result {
	srv := s.srv
	H := srv.doc.Hash()
	// 1. convergence, 5. valid tree
	for _, c := range s.cl {
		if c.last != srv.seq {
			s.fail("client %d stuck at seq %d (server %d)", c.id, c.last, srv.seq)
		}
		if len(c.pending) > 0 {
			s.fail("client %d still has %d pending ops after quiescence", c.id, len(c.pending))
		}
		if h := c.conf.Hash(); h != H {
			s.fail("DIVERGENCE client %d hash %s != server %s", c.id, h[:8], H[:8])
		}
		invariants(c.conf, fmt.Sprintf("client%d", c.id), s.fail)
	}
	invariants(srv.doc, "server", s.fail)
	// 2. replay equivalence (+ snapshot resume) and idempotency of double delivery
	if h := fold(srv.log, nil).Hash(); h != H {
		s.fail("REPLAY mismatch: fold(log) %s != server %s", h[:8], H[:8])
	}
	if k := len(srv.log) / 2; k > 0 {
		snap := fold(srv.log[:k], nil).Clone()
		if h := fold(srv.log[k:], snap).Hash(); h != H {
			s.fail("SNAPSHOT-RESUME mismatch at %d", k)
		}
	}
	twice := append(append([]*Op{}, srv.log...), srv.log...)
	if h := fold(twice, nil).Hash(); h != H {
		s.fail("IDEMPOTENCY: applying the log twice changes the state")
	}
	// 6. independent reference model: lost update, existence, structure, intent
	ref := foldRef(srv.log, s.m)
	intent := 0
	for id, rcell := range ref.cells {
		dc, ok := srv.doc.Cells[id]
		if !ok {
			s.fail("REF: cell %s missing in doc", id)
			continue
		}
		if rcell.alive != dc.alive() || (rcell.alive && s.pageDead(dc.Page)) && false {
			s.fail("REF: cell %s alive=%v in ref but %v in doc", id, rcell.alive, dc.alive())
			continue
		}
		if !rcell.alive {
			continue
		}
		if rcell.parent != dc.Parent.V || rcell.order != dc.Order.V {
			s.fail("REF: cell %s structure ref(parent=%q,order=%q) doc(parent=%q,order=%q)", id, rcell.parent, rcell.order, dc.Parent.V, dc.Order.V)
		}
		for k, v := range rcell.props {
			if dc.Props[k].V != v {
				s.fail("LOST UPDATE: cell %s key %s ref=%q doc=%q", id, k, v, dc.Props[k].V)
			}
		}
		if dc.Props["src"].V != rcell.src || dc.Props["tgt"].V != rcell.tgt {
			s.fail("REF: edge %s terminals ref(%q,%q) doc(%q,%q)", id, rcell.src, rcell.tgt, dc.Props["src"].V, dc.Props["tgt"].V)
		}
		ax, ay := srv.doc.Abs(dc)
		if ax != rcell.x || ay != rcell.y {
			intent++
		}
	}
	for id, p := range ref.pages {
		dp := srv.doc.Pages[id]
		if dp == nil || (dp.Dead == 0) != p.alive || (p.alive && (dp.Name.V != p.name || dp.Order.V != p.order)) {
			s.fail("REF: page %s mismatch", id)
		}
	}
	if s.sc.Intent && intent > 0 {
		s.fail("WRONG INTENT (INTENT mode): %d cell(s) ended at a position different from the author's intent", intent)
	}
	// 8. permissions: every logged op must have been allowed when it was sequenced
	for _, m := range srv.meta {
		o := m.op
		if o.Actor < 0 {
			continue
		}
		page := o.Page
		if page == "" {
			if c := srv.doc.Cells[o.Cell]; c != nil {
				page = c.Page
			}
		}
		allowed := true
		for _, p := range s.timeline {
			if p.actor == o.Actor && p.page == page && p.t <= m.tick {
				allowed = p.allowed
			}
		}
		if !allowed {
			s.fail("AUTHZ: op %s by actor %d on %s sequenced at tick %d while revoked", o.ID, o.Actor, page, m.tick)
		}
	}
	// 9. accounting: every generated op is either applied or explicitly nacked, never both, never silently gone
	for _, c := range s.cl {
		for _, id := range c.created {
			_, applied := srv.seen[id]
			why, nacked := srv.nacked[id]
			switch {
			case applied && nacked:
				s.fail("ACCOUNTING: op %s both applied and nacked", id)
			case !applied && !nacked:
				s.fail("ACCOUNTING: op %s neither applied nor nacked (silent loss)", id)
			case nacked:
				if _, ok := c.nackedMe[id]; !ok {
					s.fail("ACCOUNTING: nack of %s (%s) never reached its author", id, why)
				}
			}
		}
	}
	s.m["log_len"] = len(srv.log)
	s.m["intent_violations"] = intent
	var sb strings.Builder
	for _, o := range srv.log {
		fmt.Fprintf(&sb, "%d|%s|%s|%s|%s|%s\n", o.Seq, o.ID, o.T, o.Cell, o.Page, o.Key)
	}
	lh := sha256.Sum256([]byte(sb.String()))
	keys := make([]string, 0)
	for k := range s.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return &Result{Failures: s.fails, Metrics: s.m, Final: H, LogHash: hex.EncodeToString(lh[:]), Intent: intent}
}

func (s *sim) pageDead(id string) bool { return s.srv.doc.Pages[id].Dead != 0 }
