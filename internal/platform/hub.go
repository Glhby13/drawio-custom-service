package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Glhby13/drawio-custom-service/internal/model"
)

// Hub keeps the in-memory state of the documents that have a session and sequences operations.
// PostgreSQL is the source of truth: every sequencing step locks the document row, catches the cache up to the head,
// validates/resolves/applies, appends the resolved ops, and only then publishes (ADR-0006/0007).
type Hub struct {
	s    *Server
	mu   sync.Mutex
	docs map[string]*docHandle
}

type writeInfo struct {
	seq   int64
	actor string
	opID  string
	at    time.Time
}

type docHandle struct {
	id         string
	mu         sync.Mutex
	doc        *model.Doc
	sessions   map[*Session]struct{}
	lastWrites map[string]writeInfo
	snapSeq    int64
	snapping   bool
	lastUse    time.Time
	lastHash   time.Time
}

func newHub(s *Server) *Hub { return &Hub{s: s, docs: map[string]*docHandle{}} }

func (h *Hub) handle(id string) *docHandle {
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.docs[id]
	if d == nil {
		d = &docHandle{id: id, sessions: map[*Session]struct{}{}, lastWrites: map[string]writeInfo{}}
		h.docs[id] = d
	}
	d.lastUse = time.Now()
	return d
}

// existing returns the handle only when it is already loaded in memory.
func (h *Hub) existing(id string) *docHandle {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.docs[id]
}

// janitor drops idle handles (no session for 10 minutes) so memory follows use.
func (h *Hub) janitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.mu.Lock()
			for id, d := range h.docs {
				d.mu.Lock()
				if len(d.sessions) == 0 && time.Since(d.lastUse) > 10*time.Minute {
					delete(h.docs, id)
				}
				d.mu.Unlock()
			}
			h.mu.Unlock()
		}
	}
}

func snapshotKey(docID string, seq int64) string {
	return fmt.Sprintf("docs/%s/snap/%d.json.gz", docID, seq)
}

// ensure loads the document (latest snapshot + log tail). Caller holds dh.mu.
func (dh *docHandle) ensure(ctx context.Context, s *Server) error {
	if dh.doc != nil {
		return nil
	}
	seq, key, err := s.store.LatestSnapshot(ctx, dh.id)
	if err != nil {
		return fmt.Errorf("no snapshot for %s: %w", dh.id, err)
	}
	raw, err := s.blob.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", key, err)
	}
	doc := model.NewDoc()
	if err := json.Unmarshal(raw, doc); err != nil {
		return err
	}
	doc.Seq = seq
	var head int64
	if err := s.store.pool.QueryRow(ctx, `SELECT head_seq FROM documents WHERE id=$1`, dh.id).Scan(&head); err != nil {
		return err
	}
	if head > seq {
		tail, err := s.store.LoadOps(ctx, dh.id, seq, head)
		if err != nil {
			return err
		}
		for i := range tail {
			doc.Apply(&tail[i])
			doc.Seq = tail[i].Seq
		}
	}
	dh.doc, dh.snapSeq = doc, seq
	return nil
}

// catchUp applies ops committed by another replica. Caller holds dh.mu.
func (dh *docHandle) catchUp(ctx context.Context, s *Server) error {
	var head int64
	if err := s.store.pool.QueryRow(ctx, `SELECT head_seq FROM documents WHERE id=$1`, dh.id).Scan(&head); err != nil {
		return err
	}
	if dh.doc.Seq < head {
		tail, err := s.store.LoadOps(ctx, dh.id, dh.doc.Seq, head)
		if err != nil {
			return err
		}
		for i := range tail {
			dh.doc.Apply(&tail[i])
			dh.doc.Seq = tail[i].Seq
		}
	}
	return nil
}

// Read gives fn a consistent, current view of the document under the handle lock (fn must not retain it).
func (h *Hub) Read(ctx context.Context, id string, fn func(*model.Doc) error) error {
	dh := h.handle(id)
	dh.mu.Lock()
	defer dh.mu.Unlock()
	if err := dh.ensure(ctx, h.s); err != nil {
		return err
	}
	if err := dh.catchUp(ctx, h.s); err != nil {
		return err
	}
	return fn(dh.doc)
}

// SeqMode selects the behaviour on a failing op.
type SeqMode int

const (
	// SeqAtomic rejects the whole batch when one op fails.
	SeqAtomic SeqMode = iota
	// SeqBestEffort applies what still applies and reports the rest (used by revert).
	SeqBestEffort
)

// SeqResult is the outcome of one sequencing call.
type SeqResult struct {
	Applied    []model.Op
	Err        *model.OpError
	Skipped    []model.OpError
	Duplicate  bool
	Overridden []Overridden
	Hash       string
}

// Overridden tells the author of an earlier, concurrent write that someone else's op replaced it.
type Overridden struct {
	OpID  string `json:"opId"`
	Actor string `json:"actor"`
	By    string `json:"by"`
	ByOp  string `json:"byOp"`
	Cell  string `json:"cell,omitempty"`
	Page  string `json:"page,omitempty"`
	Key   string `json:"key,omitempty"`
}

func writeKey(o *model.Op) string {
	switch o.T {
	case model.OpSetProp:
		return o.Page + "/" + o.Cell + "/" + o.Key
	case model.OpSetPos:
		return o.Page + "/" + o.Cell + "/pos"
	case model.OpReparent:
		return o.Page + "/" + o.Cell + "/parent"
	case model.OpReorder:
		return o.Page + "/" + o.Cell + "/order"
	case model.OpConnect:
		return o.Page + "/" + o.Cell + "/" + o.End
	case model.OpRenamePage:
		return o.Page + "//name"
	}
	return ""
}

var errReload = errors.New("document cache invalid")

func firstOr(sk []model.OpError) *model.OpError {
	if len(sk) > 0 {
		return &sk[0]
	}
	return &model.OpError{Code: "nothing_applied"}
}

// Sequence validates, resolves, sequences, persists and fans out a batch.
func (s *Server) Sequence(ctx context.Context, docID string, meta *DocMeta, p *Principal, batch string, ops []model.Op, base int64, mode SeqMode) (*SeqResult, error) {
	dh := s.hub.handle(docID)
	dh.mu.Lock()
	defer dh.mu.Unlock()
	var res *SeqResult
	var work *model.Doc
	for attempt := 0; attempt < 2; attempt++ {
		if err := dh.ensure(ctx, s); err != nil {
			return nil, err
		}
		res, work = nil, nil
		err := pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
			var head int64
			if err := tx.QueryRow(ctx, `SELECT head_seq FROM documents WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, docID).Scan(&head); err != nil {
				return err
			}
			if dh.doc.Seq > head {
				return errReload
			}
			if dh.doc.Seq < head {
				tail, err := s.store.LoadOps(ctx, docID, dh.doc.Seq, head)
				if err != nil {
					return err
				}
				for i := range tail {
					dh.doc.Apply(&tail[i])
					dh.doc.Seq = tail[i].Seq
				}
			}
			// idempotency: a re-sent batch (lost ack) must not be applied twice
			ids := make([]string, len(ops))
			for i := range ops {
				ids[i] = ops[i].ID
			}
			rows, err := tx.Query(ctx, `SELECT seq FROM ops WHERE document_id=$1 AND op_id = ANY($2) ORDER BY seq`, docID, ids)
			if err != nil {
				return err
			}
			var dupSeqs []int64
			for rows.Next() {
				var q int64
				if err := rows.Scan(&q); err != nil {
					rows.Close()
					return err
				}
				dupSeqs = append(dupSeqs, q)
			}
			rows.Close()
			if len(dupSeqs) > 0 {
				if len(dupSeqs) != len(ids) {
					res = &SeqResult{Err: &model.OpError{Index: 0, Code: "partial_duplicate"}}
					return nil
				}
				stored, err := s.store.LoadBatchBySeqs(ctx, docID, dupSeqs)
				if err != nil {
					return err
				}
				res = &SeqResult{Applied: stored, Duplicate: true}
				return nil
			}
			stamped := make([]model.Op, len(ops))
			for i := range ops {
				stamped[i] = ops[i]
				a := p.Actor
				stamped[i].Actor, stamped[i].Batch = &a, batch
			}
			target := dh.doc
			if p.IsAI || mode == SeqBestEffort {
				target = dh.doc.Clone()
			}
			var applied []model.Op
			var skipped []model.OpError
			if mode == SeqBestEffort {
				applied, skipped = target.ApplyBestEffort(stamped)
			} else {
				var oe *model.OpError
				applied, oe = target.ApplyBatch(stamped)
				if oe != nil {
					res = &SeqResult{Err: oe}
					return nil //nolint:nilerr // a rejected batch is a normal result (Err), not a transaction failure; the tx just ends
				}
			}
			if len(applied) == 0 {
				res = &SeqResult{Skipped: skipped, Err: firstOr(skipped)}
				return nil
			}
			if p.IsAI {
				deleted := 0
				for i := range applied {
					if applied[i].T == model.OpDeleteCell && applied[i].Res != nil {
						deleted += len(applied[i].Res.Del)
					}
				}
				if deleted > meta.AI.MaxDelete {
					res = &SeqResult{Err: &model.OpError{Index: 0, Code: "confirmation_required", Msg: fmt.Sprintf("this batch would delete %d cells; the limit for AI actors is %d", deleted, meta.AI.MaxDelete)}}
					return nil
				}
			}
			b := &pgx.Batch{}
			for i := range applied {
				o := &applied[i]
				raw, _ := json.Marshal(o)
				b.Queue(`INSERT INTO ops (document_id, seq, op_id, batch, actor_id, actor_type, obo, agent, model, type, op) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),$10,$11)`,
					docID, o.Seq, o.ID, batch, o.Actor.ID, o.Actor.Type, o.Actor.OnBehalfOf, o.Actor.Agent, o.Actor.Model, o.T, raw)
			}
			br := tx.SendBatch(ctx, b)
			for range applied {
				if _, err := br.Exec(); err != nil {
					_ = br.Close()
					return err
				}
			}
			if err := br.Close(); err != nil {
				return err
			}
			last := applied[len(applied)-1].Seq
			if _, err := tx.Exec(ctx, `UPDATE documents SET head_seq=$2, updated_at=now() WHERE id=$1`, docID, last); err != nil {
				return err
			}
			res = &SeqResult{Applied: applied, Skipped: skipped}
			if target != dh.doc {
				work = target
			}
			return nil
		})
		if errors.Is(err, errReload) {
			dh.doc = nil
			continue
		}
		if err != nil {
			dh.doc = nil // the cache may hold ops that never committed
			return nil, err
		}
		break
	}
	if res == nil {
		return nil, errReload
	}
	if res.Err != nil || res.Duplicate || len(res.Applied) == 0 {
		return res, nil
	}
	if work != nil {
		dh.doc = work
	}
	last := res.Applied[len(res.Applied)-1].Seq
	dh.doc.Seq = last
	// concurrent-overwrite detection (best effort, memory only)
	for i := range res.Applied {
		o := &res.Applied[i]
		k := writeKey(o)
		if k == "" {
			continue
		}
		if w, ok := dh.lastWrites[k]; ok && w.actor != o.Actor.ID && w.seq > base && time.Since(w.at) < 5*time.Minute {
			res.Overridden = append(res.Overridden, Overridden{OpID: w.opID, Actor: w.actor, By: o.Actor.ID, ByOp: o.ID, Cell: o.Cell, Page: o.Page, Key: o.Key})
		}
		dh.lastWrites[k] = writeInfo{seq: o.Seq, actor: o.Actor.ID, opID: o.ID, at: time.Now()}
	}
	if len(dh.lastWrites) > 20000 {
		dh.lastWrites = map[string]writeInfo{}
	}
	if last%25 < int64(len(res.Applied)) || time.Since(dh.lastHash) > 3*time.Second {
		res.Hash, dh.lastHash = dh.doc.Hash(), time.Now()
	}
	dh.broadcastOps(res.Applied, res.Hash, res.Overridden)
	s.publish(docID, ctlMsg{Origin: s.replica, Kind: "ops", Ops: res.Applied, Hash: res.Hash, Overridden: res.Overridden})
	if last-dh.snapSeq >= s.cfg.SnapshotEvery && !dh.snapping {
		dh.snapping = true
		raw, _ := json.Marshal(dh.doc)
		go s.writeSnapshot(docID, last, raw, dh) //nolint:gosec // outlives the request on purpose; the snapshot writer owns its own timeout context
	}
	return res, nil
}

// writeSnapshot stores a snapshot off the hot path; failure only delays compaction.
func (s *Server) writeSnapshot(docID string, seq int64, raw []byte, dh *docHandle) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	key := snapshotKey(docID, seq)
	n, err := s.blob.Put(ctx, key, raw)
	if err == nil {
		sum := sha256.Sum256(raw)
		err = s.store.AddSnapshot(ctx, docID, seq, key, hex.EncodeToString(sum[:]), n)
	}
	dh.mu.Lock()
	dh.snapping = false
	if err == nil && seq > dh.snapSeq {
		dh.snapSeq = seq
	}
	dh.mu.Unlock()
	if err != nil {
		s.log.Warn("snapshot failed", "doc", docID, "seq", seq, "err", err.Error())
	}
}

// LoadBatchBySeqs returns stored ops by sequence number.
func (s *Store) LoadBatchBySeqs(ctx context.Context, docID string, seqs []int64) ([]model.Op, error) {
	rows, err := s.pool.Query(ctx, `SELECT op FROM ops WHERE document_id=$1 AND seq = ANY($2) ORDER BY seq`, docID, seqs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Op
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var o model.Op
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// restoreTo replaces the document state with a version's state as one new sequence number (resync for every client).
func (s *Server) restoreTo(ctx context.Context, docID string, p *Principal, versionID string, old *model.Doc) (int64, error) {
	dh := s.hub.handle(docID)
	dh.mu.Lock()
	defer dh.mu.Unlock()
	if err := dh.ensure(ctx, s); err != nil {
		return 0, err
	}
	var newSeq int64
	var next *model.Doc
	err := pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		var head int64
		if err := tx.QueryRow(ctx, `SELECT head_seq FROM documents WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, docID).Scan(&head); err != nil {
			return err
		}
		newSeq = head + 1
		next = old.Clone()
		next.Seq = newSeq
		a := p.Actor
		raw, _ := json.Marshal(model.Op{ID: "restore-" + newID(6), T: "restore", Key: versionID, Seq: newSeq, Actor: &a})
		if _, err := tx.Exec(ctx, `INSERT INTO ops (document_id, seq, op_id, batch, actor_id, actor_type, obo, agent, model, type, op) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),'restore',$10)`,
			docID, newSeq, "restore-"+newID(6), "restore-"+versionID+"-"+newID(4), a.ID, a.Type, a.OnBehalfOf, a.Agent, a.Model, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE documents SET head_seq=$2, updated_at=now() WHERE id=$1`, docID, newSeq); err != nil {
			return err
		}
		// the snapshot is part of the commit: replay never has to interpret a restore op
		raw2, _ := json.Marshal(next)
		key := snapshotKey(docID, newSeq)
		n, err := s.blob.Put(ctx, key, raw2)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw2)
		if _, err := tx.Exec(ctx, `INSERT INTO snapshots (document_id, seq, s3_key, sha256, size) VALUES ($1,$2,$3,$4,$5)`, docID, newSeq, key, hex.EncodeToString(sum[:]), n); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE documents SET snapshot_seq=$2 WHERE id=$1`, docID, newSeq)
		return err
	})
	if err != nil {
		dh.doc = nil
		return 0, err
	}
	dh.doc, dh.snapSeq = next, newSeq
	dh.lastWrites = map[string]writeInfo{}
	dh.broadcastResync("restore")
	s.publish(docID, ctlMsg{Origin: s.replica, Kind: "resync"})
	return newSeq, nil
}
