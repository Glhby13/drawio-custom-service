package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/Glhby13/drawio-custom-service/internal/model"
)

// Out is every server->client message (one struct, omitempty keeps frames small).
type Out struct {
	T          string          `json:"t"`
	Mode       string          `json:"mode,omitempty"` // joined: full | catchup
	Seq        int64           `json:"seq,omitempty"`
	Hash       string          `json:"hash,omitempty"`
	State      json.RawMessage `json:"state,omitempty"`
	XML        string          `json:"xml,omitempty"`
	Role       string          `json:"role,omitempty"`
	Title      string          `json:"title,omitempty"`
	Ops        []model.Op      `json:"ops,omitempty"`
	Op         *model.Op       `json:"op,omitempty"`
	Batch      string          `json:"batch,omitempty"`
	Seqs       []int64         `json:"seqs,omitempty"`
	Index      int             `json:"index,omitempty"`
	Code       string          `json:"code,omitempty"`
	Msg        string          `json:"msg,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Presence   []PresenceEntry `json:"presence,omitempty"`
	You        string          `json:"you,omitempty"`
	Overridden *Overridden     `json:"overridden,omitempty"`
}

// In is every client->server message.
type In struct {
	T     string     `json:"t"`
	Batch string     `json:"batch,omitempty"`
	Base  int64      `json:"base,omitempty"`
	Ops   []model.Op `json:"ops,omitempty"`
	Since int64      `json:"since,omitempty"`
}

// PresenceEntry is one connected collaborator (human or AI).
type PresenceEntry struct {
	SID   string `json:"sid"`
	User  string `json:"user"`
	Name  string `json:"name,omitempty"`
	Type  string `json:"type"`
	OBO   string `json:"obo,omitempty"`
	Agent string `json:"agent,omitempty"`
	Role  string `json:"role"`
	Since int64  `json:"since"`
}

// Session is one WebSocket connection.
type Session struct {
	id      string
	s       *Server
	dh      *docHandle
	conn    *websocket.Conn
	p       *Principal
	role    atomic.Value // string
	send    chan []byte
	closed  atomic.Bool
	cancel  context.CancelFunc
	started time.Time
}

func (se *Session) getRole() string { r, _ := se.role.Load().(string); return r }

func (se *Session) enqueue(o Out) {
	if se.closed.Load() {
		return
	}
	raw, _ := json.Marshal(o)
	select {
	case se.send <- raw:
	default:
		se.close(4408, "slow consumer")
	}
}

func (se *Session) close(code int, reason string) {
	if se.closed.Swap(true) {
		return
	}
	go func() {
		_ = se.conn.Close(websocket.StatusCode(code), reason)
		se.cancel()
	}()
}

// ---- fan-out (callers hold dh.mu) -------------------------------------------------------------------------------

func (dh *docHandle) broadcastOps(ops []model.Op, hash string, overridden []Overridden) {
	for se := range dh.sessions {
		for i := range ops {
			o := ops[i]
			msg := Out{T: "op", Op: &o}
			if i == len(ops)-1 {
				msg.Hash = hash
			}
			se.enqueue(msg)
		}
		for i := range overridden {
			if overridden[i].Actor == se.p.Username {
				ov := overridden[i]
				se.enqueue(Out{T: "overridden", Overridden: &ov})
			}
		}
	}
}

func (dh *docHandle) broadcastResync(reason string) {
	for se := range dh.sessions {
		se.enqueue(Out{T: "resync", Reason: reason})
	}
}

func (dh *docHandle) broadcastPresence(s *Server) {
	list := s.presenceList(dh.id)
	for se := range dh.sessions {
		se.enqueue(Out{T: "presence", Presence: list})
	}
}

// reauthorize re-evaluates every session after a permission/policy change.
func (dh *docHandle) reauthorize(s *Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	meta, err := s.store.GetDocument(ctx, dh.id)
	for se := range dh.sessions {
		if err != nil {
			se.close(4404, "document gone")
			continue
		}
		role, err := s.docRole(ctx, se.p, meta)
		if err != nil {
			continue
		}
		if !s.can(se.p, role, ActRead) {
			se.close(4403, "access revoked")
			continue
		}
		if role != se.getRole() {
			se.role.Store(role)
			se.enqueue(Out{T: "role", Role: role, Title: meta.Title})
		}
	}
}

// ---- presence in Redis (shared by replicas) ----------------------------------------------------------------------

func presenceKey(docID string) string { return "drawio:pres:" + docID }

func (s *Server) presenceSet(ctx context.Context, docID string, e PresenceEntry) {
	raw, _ := json.Marshal(e)
	s.rdb.ZAdd(ctx, presenceKey(docID), redis.Z{Score: float64(time.Now().Add(70 * time.Second).Unix()), Member: string(raw)})
	s.rdb.Expire(ctx, presenceKey(docID), 5*time.Minute)
}

func (s *Server) presenceDel(ctx context.Context, docID string, e PresenceEntry) {
	raw, _ := json.Marshal(e)
	s.rdb.ZRem(ctx, presenceKey(docID), string(raw))
}

func (s *Server) presenceList(docID string) []PresenceEntry {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := float64(time.Now().Unix())
	s.rdb.ZRemRangeByScore(ctx, presenceKey(docID), "-inf", "("+formatFloat(now))
	members, err := s.rdb.ZRangeArgs(ctx, redis.ZRangeArgs{Key: presenceKey(docID), Start: formatFloat(now), Stop: "+inf", ByScore: true}).Result()
	out := []PresenceEntry{}
	if err != nil {
		return out
	}
	for _, m := range members {
		var e PresenceEntry
		if json.Unmarshal([]byte(m), &e) == nil {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since < out[j].Since })
	return out
}

func formatFloat(f float64) string { return json.Number(jsonFloat(f)).String() }

func jsonFloat(f float64) string { b, _ := json.Marshal(f); return string(b) }

// ---- the WebSocket endpoint ----------------------------------------------------------------------------------------

func (s *Server) wsHandler(w http.ResponseWriter, r *http.Request) {
	p, err := s.authn(r)
	if err != nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	docID := r.PathValue("id")
	meta, err := s.store.GetDocument(r.Context(), docID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	role, err := s.docRole(r.Context(), p, meta)
	if err != nil {
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	if !s.can(p, role, ActRead) {
		_ = s.store.Audit(r.Context(), p.Actor, docID, "ws.join", "denied", map[string]string{"role": role})
		http.Error(w, "not found", http.StatusNotFound) // same answer as REST: no existence oracle
		return
	}
	// The browser origin was verified by the gateway (CSRF/origin rule, ADR-0004); this service is ClusterIP-only and
	// requires a signed internal token, so the library's own Host/Origin comparison would only see the proxy hop.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	c.SetReadLimit(8 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	se := &Session{id: newID(6), s: s, conn: c, p: p, send: make(chan []byte, 1024), cancel: cancel, started: time.Now()}
	se.role.Store(role)
	go se.writeLoop(ctx)
	defer func() {
		se.closed.Store(true)
		cancel()
		_ = c.CloseNow()
	}()
	se.readLoop(ctx, meta)
}

func (se *Session) writeLoop(ctx context.Context) {
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-se.send:
			wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := se.conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				se.close(1011, "write failed")
				return
			}
		case <-ping.C:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := se.conn.Ping(wctx)
			cancel()
			if err != nil {
				se.close(1011, "ping failed")
				return
			}
			if se.dh != nil {
				pctx, pcancel := context.WithTimeout(ctx, 3*time.Second)
				se.s.presenceSet(pctx, se.dh.id, se.entry())
				pcancel()
			}
		}
	}
}

func (se *Session) entry() PresenceEntry {
	return PresenceEntry{SID: se.id, User: se.p.Username, Name: se.p.Name, Type: se.p.Actor.Type, OBO: se.p.Actor.OnBehalfOf, Agent: se.p.Actor.Agent, Role: se.getRole(), Since: se.started.Unix()}
}

func (se *Session) readLoop(ctx context.Context, meta *DocMeta) {
	s := se.s
	maxLife := time.NewTimer(2 * time.Hour)
	defer maxLife.Stop()
	for {
		_, data, err := se.conn.Read(ctx)
		if err != nil {
			se.leave()
			return
		}
		var in In
		if json.Unmarshal(data, &in) != nil {
			se.enqueue(Out{T: "error", Code: "bad_json"})
			continue
		}
		switch in.T {
		case "join":
			if se.dh != nil {
				continue
			}
			if err := se.join(ctx, meta, in.Since); err != nil {
				s.log.Warn("join failed", "doc", meta.ID, "err", err.Error())
				se.enqueue(Out{T: "error", Code: "join_failed", Msg: "document could not be loaded"})
				se.close(1011, "join failed")
				return
			}
		case "ping":
			se.enqueue(Out{T: "pong"})
		case "ops":
			if se.dh == nil {
				se.enqueue(Out{T: "error", Code: "not_joined"})
				continue
			}
			se.handleOps(ctx, meta, in)
		default:
			se.enqueue(Out{T: "error", Code: "unknown_message"})
		}
		select {
		case <-maxLife.C:
			se.close(4401, "session expired, reconnect")
			return
		default:
		}
	}
}

func (se *Session) join(ctx context.Context, meta *DocMeta, since int64) error {
	s := se.s
	dh := s.hub.handle(meta.ID)
	dh.mu.Lock()
	defer dh.mu.Unlock()
	if err := dh.ensure(ctx, s); err != nil {
		return err
	}
	if err := dh.catchUp(ctx, s); err != nil {
		return err
	}
	out := Out{T: "joined", Seq: dh.doc.Seq, Role: se.getRole(), Title: meta.Title, You: se.id}
	if since > 0 && since <= dh.doc.Seq && dh.doc.Seq-since <= 2000 {
		tail, err := s.store.LoadOps(ctx, meta.ID, since, dh.doc.Seq)
		if err == nil && int64(len(tail)) == dh.doc.Seq-since {
			resync := false
			for i := range tail {
				if tail[i].T == "restore" {
					resync = true
				}
			}
			if !resync {
				out.Mode, out.Ops = "catchup", tail
			}
		}
	}
	if out.Mode == "" {
		out.Mode = "full"
		out.State, out.XML, out.Hash = dh.doc.StateJSON(), dh.doc.ExportXML(), dh.doc.Hash()
	}
	se.dh = dh
	dh.sessions[se] = struct{}{}
	se.enqueue(out)
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s.presenceSet(pctx, meta.ID, se.entry())
	dh.broadcastPresence(s)
	s.publish(meta.ID, ctlMsg{Origin: s.replica, Kind: "presence"})
	_ = s.store.Audit(pctx, se.p.Actor, meta.ID, "ws.join", "ok", map[string]any{"role": se.getRole(), "mode": out.Mode})
	return nil
}

func (se *Session) leave() {
	if se.dh == nil {
		return
	}
	s := se.s
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.presenceDel(ctx, se.dh.id, se.entry())
	se.dh.mu.Lock()
	delete(se.dh.sessions, se)
	se.dh.lastUse = time.Now()
	se.dh.broadcastPresence(s)
	se.dh.mu.Unlock()
	s.publish(se.dh.id, ctlMsg{Origin: s.replica, Kind: "presence"})
}

func (se *Session) handleOps(ctx context.Context, meta *DocMeta, in In) {
	s := se.s
	nack := func(code, msg string, idx int) {
		se.enqueue(Out{T: "nack", Batch: in.Batch, Code: code, Msg: msg, Index: idx})
		_ = s.store.Audit(ctx, se.p.Actor, meta.ID, "ops", "rejected:"+code, map[string]any{"batch": in.Batch, "ops": len(in.Ops)})
	}
	if in.Batch == "" || len(in.Batch) > 64 || len(in.Ops) == 0 || len(in.Ops) > 500 {
		nack("bad_batch", "batch id and 1..500 ops required", 0)
		return
	}
	if !s.rate.allow("ops:" + se.p.Username) {
		nack("rate_limited", "too many operations", 0)
		return
	}
	// the role is re-read from the DB-backed PDP for every batch (a revoked share must stop writes immediately)
	fresh, err := s.store.GetDocument(ctx, meta.ID)
	if err != nil {
		nack("document_gone", "", 0)
		return
	}
	role, err := s.docRole(ctx, se.p, fresh)
	if err != nil {
		nack("internal", "", 0)
		return
	}
	se.role.Store(role)
	if !s.can(se.p, role, ActEdit) {
		code := "forbidden"
		if se.p.IsAI && !fresh.AI.Allowed {
			code = "ai_disabled"
		}
		nack(code, "your role does not allow editing", 0)
		return
	}
	if !s.aiQuota(ctx, fresh, se.p, len(in.Ops)) {
		nack("ai_rate_limited", "AI operation quota exceeded", 0)
		return
	}
	res, err := s.Sequence(ctx, meta.ID, fresh, se.p, in.Batch, in.Ops, in.Base, SeqAtomic)
	if err != nil {
		s.log.Error("sequence failed", "doc", meta.ID, "err", err.Error())
		nack("internal", "", 0)
		return
	}
	if res.Err != nil {
		nack(res.Err.Code, res.Err.Msg, res.Err.Index)
		return
	}
	seqs := make([]int64, len(res.Applied))
	for i := range res.Applied {
		seqs[i] = res.Applied[i].Seq
	}
	se.enqueue(Out{T: "ack", Batch: in.Batch, Seqs: seqs})
	if res.Duplicate {
		return
	}
	if se.p.IsAI { // human edits are attributed by the operation log itself; AI batches also get an audit row
		_ = s.store.Audit(ctx, se.p.Actor, meta.ID, "ops", "ok", map[string]any{"batch": in.Batch, "ops": len(res.Applied), "fromSeq": seqs[0], "toSeq": seqs[len(seqs)-1]})
	}
}
