// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-002 §fuzz).
// Deterministic discrete-event simulation: one server sequencer + N clients with optimistic local state
// (confirmed + pending re-applied), FIFO links with delay, offline periods, reconnect catch-up, retransmission
// (duplicate delivery), live permission changes. Everything derives from the Script, so runs are reproducible and
// shrinkable.
package main

import (
	"fmt"
	"hash/crc32"
	"math/rand"
	"sort"
	"strconv"
)

type Event struct {
	T    int    `json:"t"`
	C    int    `json:"c"`
	Kind int    `json:"k"`
	A    uint32 `json:"a"`
	B    uint32 `json:"b"`
	D    uint32 `json:"d"`
}

type Script struct {
	Seed    int64   `json:"seed"`
	Clients int     `json:"clients"`
	Intent  bool    `json:"intent"` // true: INTENT geometry (abs targets, keep-abs reparent); false: NAIVE (relative values authored by the client)
	Events  []Event `json:"events"`
}

const (
	kEdit = iota
	kOffline
	kOnline
	kRevoke
	kGrant
	kRetransmit
)

type msg struct {
	at  int
	op  *Op
	typ string // "op" | "ack" | "nack" | "sync"
	why string
	seq int64 // sync: client's last seq
}

type opMeta struct {
	op      *Op
	tick    int
	hash    string // sampled
	granted bool
}

type server struct {
	doc    *Doc
	seq    int64
	log    []*Op
	meta   map[int64]*opMeta
	seen   map[string]int64  // op id -> seq (applied)
	nacked map[string]string // op id -> reason
}

type client struct {
	id       int
	online   bool
	conf     *Doc
	last     int64
	pending  []*Op
	n        int
	cseq     int
	toS, toC []msg
	lastAtS  int
	lastAtC  int
	created  []string
	nackedMe map[string]string
}

type perm struct {
	t, actor int
	page     string
	allowed  bool
}

type Result struct {
	Failures []string
	Metrics  map[string]int
	Final    string
	LogHash  string
	Intent   int // abs-position oracle mismatches (reported, only a failure in INTENT mode)
}

type sim struct {
	sc       *Script
	rng      *rand.Rand
	srv      *server
	cl       []*client
	t        int
	perms    map[string]bool
	timeline []perm
	m        map[string]int
	fails    []string
}

func (s *sim) fail(f string, a ...any) {
	if len(s.fails) < 20 {
		s.fails = append(s.fails, fmt.Sprintf(f, a...))
	}
}

func permKey(actor int, page string) string { return strconv.Itoa(actor) + "/" + page }

func (s *sim) allowed(actor int, page string) bool {
	if v, ok := s.perms[permKey(actor, page)]; ok {
		return v
	}
	return true
}

func newSim(sc *Script) *sim {
	s := &sim{sc: sc, rng: rand.New(rand.NewSource(sc.Seed)), perms: map[string]bool{}, m: map[string]int{}}
	s.srv = &server{doc: NewDoc(), meta: map[int64]*opMeta{}, seen: map[string]int64{}, nacked: map[string]string{}}
	// initial pages are ordinary sequenced ops so that replay from an empty doc reproduces everything
	for i, pg := range []struct{ id, key string }{{"P0", "m"}, {"P1", "t"}} {
		op := &Op{ID: "init" + strconv.Itoa(i), Actor: -1, T: "addPage", Page: pg.id, Key: pg.key, Name: pg.id}
		s.sequence(op, 0)
	}
	for i := 0; i < sc.Clients; i++ {
		c := &client{id: i, online: true, conf: NewDoc(), nackedMe: map[string]string{}}
		for _, op := range s.srv.log {
			c.conf.Apply(op)
			c.last = op.Seq
		}
		s.cl = append(s.cl, c)
	}
	return s
}

func (s *sim) sequence(op *Op, tick int) {
	s.srv.seq++
	*op = *s.srv.doc.Resolve(op) // the log stores the RESOLVED op
	op.Seq = s.srv.seq
	s.srv.doc.Apply(op)
	s.srv.log = append(s.srv.log, op)
	s.srv.seen[op.ID] = op.Seq
	m := &opMeta{op: op, tick: tick}
	if op.Seq%5 == 0 {
		m.hash = s.srv.doc.Hash()
	}
	s.srv.meta[op.Seq] = m
}

// ---- links -------------------------------------------------------------------------------------------------------

func (s *sim) sendS(c *client, m msg) { // client -> server (FIFO, delayed)
	at := s.t + 1 + s.rng.Intn(6)
	if at < c.lastAtS {
		at = c.lastAtS
	}
	c.lastAtS = at
	m.at = at
	c.toS = append(c.toS, m)
}

func (s *sim) sendC(c *client, m msg) { // server -> client
	if !c.online {
		return // lost; recovered by sync on reconnect
	}
	at := s.t + 1 + s.rng.Intn(6)
	if at < c.lastAtC {
		at = c.lastAtC
	}
	c.lastAtC = at
	m.at = at
	c.toC = append(c.toC, m)
}

// ---- server ---------------------------------------------------------------------------------------------------------

func (s *sim) serverRecv(c *client, m msg) {
	switch m.typ {
	case "sync":
		for _, op := range s.srv.log {
			if op.Seq > m.seq {
				s.sendC(c, msg{typ: "op", op: op})
			}
		}
		for _, p := range c.pending { // re-deliver nacks for ops this client still holds (pending order = deterministic)
			if why, ok := s.srv.nacked[p.ID]; ok {
				s.sendC(c, msg{typ: "nack", op: p, why: why})
			}
		}
	case "op":
		op := m.op
		if seq, ok := s.srv.seen[op.ID]; ok { // duplicate: idempotency via op id
			s.m["dup_ignored"]++
			if Mut.DropAckSome && seq%7 == 0 {
				return
			}
			s.sendC(c, msg{typ: "op", op: s.srv.log[seq-1]})
			return
		}
		if why, ok := s.srv.nacked[op.ID]; ok {
			s.sendC(c, msg{typ: "nack", op: op, why: why})
			return
		}
		if Mut.SilentDrop && crc32.ChecksumIEEE([]byte(op.ID))%11 == 3 && op.Actor >= 0 { // BUG: op vanishes without ack or nack
			return
		}
		cp := *op
		page := cp.Page
		if cp.Page == "" && cp.Cell != "" {
			if cc, ok := s.srv.doc.Cells[cp.Cell]; ok {
				page = cc.Page
			}
		}
		if page != "" && !Mut.NoAuthz && !s.allowed(cp.Actor, page) {
			s.nack(c, op, "forbidden")
			return
		}
		if why := s.srv.doc.Validate(&cp); why != "" {
			s.nack(c, op, why)
			return
		}
		s.sequence(&cp, s.t)
		s.srv.meta[cp.Seq].granted = true
		s.m["applied"]++
		for _, o := range s.cl {
			if Mut.DropAckSome && o == c && cp.Seq%7 == 0 {
				continue
			}
			s.sendC(o, msg{typ: "op", op: &cp})
		}
	}
}

func (s *sim) nack(c *client, op *Op, why string) {
	s.srv.nacked[op.ID] = why
	s.m["nack_"+why]++
	s.sendC(c, msg{typ: "nack", op: op, why: why})
}

// ---- client ------------------------------------------------------------------------------------------------------------

func (c *client) visible() *Doc {
	d := c.conf.Clone()
	for i, p := range c.pending {
		q := *p
		q.Seq = 1<<40 + int64(i) // pending ops win over confirmed state in the optimistic view
		if d.Validate(&q) == "" {
			d.Apply(&q) // resolves against the local view (prediction only; the server's resolution is authoritative)
		}
	}
	return d
}

func (s *sim) clientRecv(c *client, m msg) {
	switch m.typ {
	case "op":
		op := m.op
		if op.Seq <= c.last {
			return
		}
		if op.Seq != c.last+1 { // gap: ask for the missing range
			s.sendS(c, msg{typ: "sync", seq: c.last})
			return
		}
		c.conf.Apply(op)
		c.last = op.Seq
		for i, p := range c.pending {
			if p.ID == op.ID {
				c.pending = append(c.pending[:i], c.pending[i+1:]...)
				break
			}
		}
		if mm := s.srv.meta[op.Seq]; mm != nil && mm.hash != "" {
			if h := c.conf.Hash(); h != mm.hash {
				s.fail("checksum mismatch client %d at seq %d", c.id, op.Seq)
			}
		}
	case "nack":
		for i, p := range c.pending {
			if p.ID == m.op.ID {
				c.pending = append(c.pending[:i], c.pending[i+1:]...)
				c.nackedMe[p.ID] = m.why
				break
			}
		}
	}
}

// ---- edit generation (reads the client's VISIBLE state; all randomness comes from the event) -----------------------

func aliveCells(d *Doc, page string) []*Cell {
	var out []*Cell
	for _, c := range d.Cells {
		if c.alive() && (page == "" || c.Page == page) && d.Pages[c.Page].Dead == 0 {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *sim) keyBetween(d *Doc, page, parent string, r uint32) string {
	sib := d.children(page, parent)
	if Mut.UnstableSort {
		sort.Slice(sib, func(i, j int) bool { return sib[i].Order.V < sib[j].Order.V })
	}
	idx := int(r % uint32(len(sib)+1))
	a, b := "", ""
	if idx > 0 {
		a = sib[idx-1].Order.V
	}
	if idx < len(sib) {
		b = sib[idx].Order.V
	}
	if b != "" && a >= b { // concurrent duplicates can leave equal keys: fall back to appending after a
		return Between(a, "")
	}
	return Between(a, b)
}

func (s *sim) edit(c *client, e Event) {
	d := c.visible()
	pick := func(n int, r uint32) int { return int(r % uint32(n)) }
	var pages []*Page
	pages = d.SortedPages()
	if len(pages) == 0 {
		pages = nil
	}
	newOp := func(t string) *Op {
		c.n++
		c.cseq++
		return &Op{ID: fmt.Sprintf("o%d_%d", c.id, c.n), Actor: c.id, CSeq: c.cseq, Base: c.last, T: t}
	}
	emit := func(op *Op) {
		c.pending = append(c.pending, op)
		c.created = append(c.created, op.ID)
		s.m["generated"]++
		if c.online {
			s.sendS(c, msg{typ: "op", op: op})
		}
	}
	w := int(e.A % 100)
	switch {
	case w < 28: // addCell
		if len(pages) == 0 {
			return
		}
		pg := pages[pick(len(pages), e.B)]
		cs := aliveCells(d, pg.ID)
		parent := ""
		if len(cs) > 0 && e.D%2 == 0 {
			parent = cs[pick(len(cs), e.B>>8)].ID
		}
		op := newOp("addCell")
		op.Page, op.Cell, op.Par = pg.ID, fmt.Sprintf("c%d_%d", c.id, c.n), parent
		op.Kind = "ve"[boolI(e.D%5 == 0)]
		op.Key = s.keyBetween(d, pg.ID, parent, e.B>>4)
		op.Val = "v" + strconv.Itoa(int(e.D%50))
		op.X, op.Y = int(e.B%500), int((e.B>>9)%500)
		op.IX, op.IY = op.X, op.Y
		if !s.sc.Intent {
			op.Rel = true
			if parent != "" {
				px, py := d.Abs(d.Cells[parent])
				op.X, op.Y = op.X-px, op.Y-py // author computes relative coordinates in his own view
			}
		}
		emit(op)
	case w < 43: // setProp
		cs := aliveCells(d, "")
		if len(cs) == 0 {
			return
		}
		cell := cs[pick(len(cs), e.B)]
		op := newOp("setProp")
		op.Cell = cell.ID
		op.Key = []string{"value", "style:fill", "style:stroke", "attr:owner"}[e.D%4]
		op.Val = "x" + strconv.Itoa(int(e.A>>7%1000))
		emit(op)
	case w < 55: // setPos (move)
		cs := aliveCells(d, "")
		if len(cs) == 0 {
			return
		}
		cell := cs[pick(len(cs), e.B)]
		ax, ay := d.Abs(cell)
		ax, ay = ax+int(e.D%61)-30, ay+int((e.D>>8)%61)-30
		op := newOp("setPos")
		op.Cell = cell.ID
		op.X, op.Y = ax, ay
		op.IX, op.IY = ax, ay
		if !s.sc.Intent {
			op.Rel = true
			if cell.Parent.V != "" {
				if pc, ok := d.Cells[cell.Parent.V]; ok {
					px, py := d.Abs(pc)
					op.X, op.Y = ax-px, ay-py
				}
			}
		}
		emit(op)
	case w < 65: // reparent
		cs := aliveCells(d, "")
		if len(cs) < 2 {
			return
		}
		cell := cs[pick(len(cs), e.B)]
		same := aliveCells(d, cell.Page)
		parent := ""
		if e.D%4 != 0 {
			parent = same[pick(len(same), e.B>>8)].ID
		}
		if parent == cell.ID {
			return
		}
		op := newOp("reparent")
		op.Cell, op.Par = cell.ID, parent
		op.Key = s.keyBetween(d, cell.Page, parent, e.D>>4)
		ax, ay := d.Abs(cell)
		if !s.sc.Intent {
			op.Rel = true
			op.X, op.Y = ax, ay
			if parent != "" {
				if pc, ok := d.Cells[parent]; ok {
					px, py := d.Abs(pc)
					op.X, op.Y = ax-px, ay-py
				}
			}
		}
		emit(op)
	case w < 72: // reorder
		cs := aliveCells(d, "")
		if len(cs) == 0 {
			return
		}
		cell := cs[pick(len(cs), e.B)]
		op := newOp("reorder")
		op.Cell, op.Key = cell.ID, s.keyBetween(d, cell.Page, cell.Parent.V, e.D)
		emit(op)
	case w < 80: // deleteCell
		cs := aliveCells(d, "")
		if len(cs) == 0 {
			return
		}
		op := newOp("deleteCell")
		op.Cell = cs[pick(len(cs), e.B)].ID
		emit(op)
	case w < 87: // connect
		cs := aliveCells(d, "")
		var edges []*Cell
		for _, x := range cs {
			if x.Kind == 'e' {
				edges = append(edges, x)
			}
		}
		if len(edges) == 0 {
			return
		}
		edge := edges[pick(len(edges), e.B)]
		same := aliveCells(d, edge.Page)
		op := newOp("connect")
		op.Cell = edge.ID
		op.Term = []string{"src", "tgt"}[e.D%2]
		if e.D%7 != 0 {
			op.TermV = same[pick(len(same), e.B>>8)].ID
		}
		emit(op)
	case w < 91: // addPage
		op := newOp("addPage")
		op.Page = fmt.Sprintf("p%d_%d", c.id, c.n)
		op.Name = "pg" + strconv.Itoa(c.n)
		a, b := "", ""
		if len(pages) > 0 {
			i := pick(len(pages)+1, e.B)
			if i > 0 {
				a = pages[i-1].Order.V
			}
			if i < len(pages) {
				b = pages[i].Order.V
			}
		}
		if b != "" && a >= b {
			b = ""
		}
		op.Key = Between(a, b)
		emit(op)
	case w < 93: // deletePage
		if len(pages) == 0 {
			return
		}
		op := newOp("deletePage")
		op.Page = pages[pick(len(pages), e.B)].ID
		emit(op)
	case w < 97: // renamePage
		if len(pages) == 0 {
			return
		}
		op := newOp("renamePage")
		op.Page, op.Name = pages[pick(len(pages), e.B)].ID, "n"+strconv.Itoa(int(e.D%100))
		emit(op)
	default: // reorderPage
		if len(pages) < 2 {
			return
		}
		pg := pages[pick(len(pages), e.B)]
		op := newOp("reorderPage")
		op.Page = pg.ID
		i := pick(len(pages)+1, e.D)
		a, b := "", ""
		if i > 0 {
			a = pages[i-1].Order.V
		}
		if i < len(pages) {
			b = pages[i].Order.V
		}
		if b != "" && a >= b {
			b = ""
		}
		op.Key = Between(a, b)
		emit(op)
	}
}

func boolI(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- main loop -------------------------------------------------------------------------------------------------------

func (s *sim) step(quiesce bool) {
	// deliver server-bound
	for _, c := range s.cl {
		for len(c.toS) > 0 && c.toS[0].at <= s.t {
			m := c.toS[0]
			c.toS = c.toS[1:]
			if m.typ == "op" && !c.online {
				continue // went offline while in flight
			}
			s.serverRecv(c, m)
		}
	}
	for _, c := range s.cl {
		for len(c.toC) > 0 && c.toC[0].at <= s.t {
			m := c.toC[0]
			c.toC = c.toC[1:]
			if c.online {
				s.clientRecv(c, m)
			}
		}
	}
}

func (s *sim) goOnline(c *client) {
	if c.online {
		return
	}
	c.online = true
	c.toS, c.toC = nil, nil
	s.m["reconnects"]++
	s.sendS(c, msg{typ: "sync", seq: c.last})
	for _, p := range c.pending {
		s.sendS(c, msg{typ: "op", op: p})
	}
}

func Run(sc *Script) *Result {
	s := newSim(sc)
	byT := map[int][]Event{}
	maxT := 0
	for _, e := range sc.Events {
		byT[e.T] = append(byT[e.T], e)
		if e.T > maxT {
			maxT = e.T
		}
	}
	for s.t = 0; s.t <= maxT; s.t++ {
		for _, e := range byT[s.t] {
			c := s.cl[e.C%sc.Clients]
			switch e.Kind {
			case kEdit:
				s.edit(c, e)
			case kOffline:
				if c.online {
					c.online, c.toS, c.toC = false, nil, nil
					s.m["offline"]++
				}
			case kOnline:
				s.goOnline(c)
			case kRevoke, kGrant:
				pg := []string{"P0", "P1"}[e.A%2]
				v := e.Kind == kGrant
				s.perms[permKey(c.id, pg)] = v
				s.timeline = append(s.timeline, perm{s.t, c.id, pg, v})
			case kRetransmit:
				if c.online {
					for _, p := range c.pending {
						s.sendS(c, msg{typ: "op", op: p})
					}
				}
			}
		}
		s.step(false)
	}
	// quiesce: everyone online, stop editing, drain
	for _, c := range s.cl {
		s.goOnline(c)
	}
	for i := 0; i < 400; i++ {
		s.t++
		s.step(true)
		busy := false
		for _, c := range s.cl {
			if len(c.toS) > 0 || len(c.toC) > 0 {
				busy = true
			}
			if len(c.pending) > 0 {
				busy = true
				if i%20 == 19 { // retransmit while unresolved
					for _, p := range c.pending {
						s.sendS(c, msg{typ: "op", op: p})
					}
					s.sendS(c, msg{typ: "sync", seq: c.last})
				}
			}
		}
		if !busy {
			break
		}
	}
	return s.check()
}

// runForLog re-runs a script and returns the server log (debug helper).
func runForLog(sc *Script) []*Op {
	s := newSim(sc)
	byT := map[int][]Event{}
	maxT := 0
	for _, e := range sc.Events {
		byT[e.T] = append(byT[e.T], e)
		if e.T > maxT {
			maxT = e.T
		}
	}
	for s.t = 0; s.t <= maxT; s.t++ {
		for _, e := range byT[s.t] {
			c := s.cl[e.C%sc.Clients]
			switch e.Kind {
			case kEdit:
				s.edit(c, e)
			case kOffline:
				if c.online {
					c.online, c.toS, c.toC = false, nil, nil
				}
			case kOnline:
				s.goOnline(c)
			case kRevoke, kGrant:
				pg := []string{"P0", "P1"}[e.A%2]
				s.perms[permKey(c.id, pg)] = e.Kind == kGrant
				s.timeline = append(s.timeline, perm{s.t, c.id, pg, e.Kind == kGrant})
			case kRetransmit:
				if c.online {
					for _, p := range c.pending {
						s.sendS(c, msg{typ: "op", op: p})
					}
				}
			}
		}
		s.step(false)
	}
	for _, c := range s.cl {
		s.goOnline(c)
	}
	for i := 0; i < 400; i++ {
		s.t++
		s.step(true)
	}
	return s.srv.log
}
