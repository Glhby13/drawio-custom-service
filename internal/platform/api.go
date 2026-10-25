package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/model"
)

var subjectRe = regexp.MustCompile(`^(\*|[A-Za-z0-9_.@\-]{1,128})$`)

func versionKey(docID, vid string) string { return "docs/" + docID + "/ver/" + vid + ".json.gz" }

// Handler returns the full HTTP handler of the platform.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /api/v1/documents", s.api(s.listDocs))
	mux.HandleFunc("POST /api/v1/documents", s.api(s.createDoc))
	mux.HandleFunc("GET /api/v1/documents/{id}", s.api(s.getDoc))
	mux.HandleFunc("PATCH /api/v1/documents/{id}", s.api(s.patchDoc))
	mux.HandleFunc("DELETE /api/v1/documents/{id}", s.api(s.deleteDoc))
	mux.HandleFunc("GET /api/v1/documents/{id}/permissions", s.api(s.getPerms))
	mux.HandleFunc("PUT /api/v1/documents/{id}/permissions", s.api(s.putPerm))
	mux.HandleFunc("PUT /api/v1/documents/{id}/ai-policy", s.api(s.putAIPolicy))
	mux.HandleFunc("GET /api/v1/documents/{id}/content", s.api(s.getContent))
	mux.HandleFunc("POST /api/v1/documents/{id}/operations", s.api(s.postOperations))
	mux.HandleFunc("POST /api/v1/documents/{id}/layout", s.api(s.postLayout))
	mux.HandleFunc("GET /api/v1/documents/{id}/history", s.api(s.getHistory))
	mux.HandleFunc("POST /api/v1/documents/{id}/revert", s.api(s.postRevert))
	mux.HandleFunc("GET /api/v1/documents/{id}/versions", s.api(s.getVersions))
	mux.HandleFunc("POST /api/v1/documents/{id}/versions", s.api(s.postVersion))
	mux.HandleFunc("POST /api/v1/documents/{id}/versions/{vid}/restore", s.api(s.postRestore))
	mux.HandleFunc("GET /api/v1/documents/{id}/comments", s.api(s.getComments))
	mux.HandleFunc("POST /api/v1/documents/{id}/comments", s.api(s.postComment))
	mux.HandleFunc("GET /api/v1/documents/{id}/audit", s.api(s.getAudit))
	mux.HandleFunc("GET /api/v1/users", s.api(s.searchUsers))
	mux.HandleFunc("GET /ws/v1/documents/{id}", s.wsHandler)
	return s.logging(mux)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	if err := s.store.pool.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := CheckSchema(ctx, s.store.pool); err != nil {
		http.Error(w, "schema out of date", http.StatusServiceUnavailable)
		return
	}
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.blob.Ping(ctx); err != nil {
		http.Error(w, "object store unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ready"))
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// Unwrap keeps websocket upgrades (Hijack) working through the wrapper.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds())
		}
	})
}

// ---- helpers ------------------------------------------------------------------------------------------------

type apiFn func(w http.ResponseWriter, r *http.Request, p *Principal)

func (s *Server) api(fn apiFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.authn(r)
		if err != nil {
			code := http.StatusUnauthorized
			if errors.Is(err, errNoAppRole) {
				code = http.StatusForbidden
			}
			writeErr(w, code, "unauthenticated", "valid internal token with a drawio role required")
			return
		}
		if !s.rate.allow("api:" + p.Username) {
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		fn(w, r, p)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"code": errCode, "message": msg}})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any, max int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

// access loads the document, computes the role and answers the PDP; it writes the error itself.
func (s *Server) access(w http.ResponseWriter, r *http.Request, p *Principal, act Action) (*DocMeta, string, bool) {
	d, err := s.store.GetDocument(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "document not found")
		} else {
			writeErr(w, http.StatusInternalServerError, "internal", "")
		}
		return nil, "", false
	}
	role, err := s.docRole(r.Context(), p, d)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return nil, "", false
	}
	if !s.can(p, role, act) {
		// a document the caller cannot even read is reported as missing (no existence oracle)
		code, ec := http.StatusForbidden, "forbidden"
		if !s.can(p, role, ActRead) {
			code, ec = http.StatusNotFound, "not_found"
		}
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, string(act), "denied", map[string]string{"role": role})
		writeErr(w, code, ec, "not allowed")
		return nil, "", false
	}
	return d, role, true
}

// ---- documents ---------------------------------------------------------------------------------------------------

func (s *Server) listDocs(w http.ResponseWriter, r *http.Request, p *Principal) {
	items, err := s.store.ListDocuments(r.Context(), p.Username, p.IsAdmin)
	if err != nil {
		s.log.Error("list documents", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if p.IsAI { // an AI actor only lists documents that explicitly allow AI and where it has its own share
		out := items[:0]
		for _, it := range items {
			d := it.DocMeta
			role, err := s.docRole(r.Context(), p, &d)
			if err == nil && rank(role) > 0 {
				it.Role = role
				out = append(out, it)
			}
		}
		items = out
	}
	writeJSON(w, http.StatusOK, map[string]any{"documents": items})
}

type createReq struct {
	Title string `json:"title"`
	XML   string `json:"xml"`
}

func (s *Server) createDoc(w http.ResponseWriter, r *http.Request, p *Principal) {
	var in createReq
	if !readJSON(w, r, &in, 12<<20) {
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Untitled diagram"
	}
	if len(title) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request", "title too long")
		return
	}
	x := in.XML
	if x == "" {
		x = model.BlankXML
	}
	doc, err := model.ImportXML(x)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_xml", err.Error())
		return
	}
	owner := p.Username
	ai := AIPolicy{Allowed: false, MaxOpsPerMin: s.cfg.AIDefaultMaxOpsPerMin, MaxDelete: s.cfg.AIDefaultMaxDelete}
	if p.IsAI {
		if p.Actor.OnBehalfOf != "" {
			owner = p.Actor.OnBehalfOf
		}
		ai.Allowed = true // a document an AI actor creates is created for AI collaboration
	}
	id := "d" + newID(8)
	raw, _ := json.Marshal(doc)
	key := snapshotKey(id, 0)
	if _, err := s.blob.Put(r.Context(), key, raw); err != nil {
		s.log.Error("create: snapshot", "err", err.Error())
		writeErr(w, http.StatusBadGateway, "storage", "object store unavailable")
		return
	}
	if err := s.store.CreateDocument(r.Context(), id, title, owner, ai); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if err := s.createSnapshotRow(r.Context(), id, key, raw); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if p.IsAI {
		_ = s.store.SetPerm(r.Context(), id, p.Username, RoleEditor, p.Username)
	}
	_ = s.store.Audit(r.Context(), p.Actor, id, "create", "ok", map[string]any{"title": title, "pages": len(doc.Pages)})
	d, _ := s.store.GetDocument(r.Context(), id)
	writeJSON(w, http.StatusCreated, map[string]any{"document": d, "role": RoleOwner})
}

func (s *Server) getDoc(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, role, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	can := map[string]bool{}
	for _, a := range []Action{ActEdit, ActComment, ActShare, ActDelete, ActRename, ActRestore, ActPolicy, ActAudit} {
		can[string(a)] = s.can(p, role, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"document": d, "role": role, "can": can, "presence": s.presenceList(d.ID)})
}

func (s *Server) patchDoc(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActRename)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &in, 4096) {
		return
	}
	t := strings.TrimSpace(in.Title)
	if t == "" || len(t) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request", "title 1..200 characters")
		return
	}
	if err := s.store.SetTitle(r.Context(), d.ID, t); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "rename", "ok", map[string]string{"from": d.Title, "to": t})
	s.notifyPerm(d.ID)
	writeJSON(w, http.StatusOK, map[string]string{"title": t})
}

func (s *Server) deleteDoc(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActDelete)
	if !ok {
		return
	}
	if err := s.store.SoftDelete(r.Context(), d.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "delete", "ok", map[string]string{"title": d.Title})
	s.notifyPerm(d.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// notifyPerm makes every session on every replica re-evaluate its role.
func (s *Server) notifyPerm(docID string) {
	if dh := s.hub.existing(docID); dh != nil {
		dh.mu.Lock()
		dh.reauthorize(s)
		dh.mu.Unlock()
	}
	s.publish(docID, ctlMsg{Origin: s.replica, Kind: "perm"})
}

// ---- sharing -------------------------------------------------------------------------------------------------------

func (s *Server) getPerms(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActShare)
	if !ok {
		return
	}
	perms, err := s.store.Perms(r.Context(), d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"owner": d.Owner, "permissions": perms, "aiPolicy": d.AI})
}

func (s *Server) putPerm(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActShare)
	if !ok {
		return
	}
	var in struct {
		Subject string `json:"subject"`
		Role    string `json:"role"` // viewer | commenter | editor | "" (remove)
	}
	if !readJSON(w, r, &in, 4096) {
		return
	}
	in.Subject = strings.TrimSpace(in.Subject)
	if !subjectRe.MatchString(in.Subject) || in.Subject == d.Owner {
		writeErr(w, http.StatusBadRequest, "bad_request", "subject must be a username or * (everyone), and not the owner")
		return
	}
	if in.Role != "" && in.Role != RoleViewer && in.Role != RoleCommenter && in.Role != RoleEditor {
		writeErr(w, http.StatusBadRequest, "bad_request", "role must be viewer, commenter, editor or empty")
		return
	}
	if err := s.store.SetPerm(r.Context(), d.ID, in.Subject, in.Role, p.Username); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "share", "ok", map[string]string{"subject": in.Subject, "role": in.Role})
	s.notifyPerm(d.ID)
	perms, _ := s.store.Perms(r.Context(), d.ID)
	writeJSON(w, http.StatusOK, map[string]any{"permissions": perms})
}

func (s *Server) putAIPolicy(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActPolicy)
	if !ok {
		return
	}
	var in AIPolicy
	if !readJSON(w, r, &in, 4096) {
		return
	}
	if in.MaxOpsPerMin < 1 || in.MaxOpsPerMin > 5000 || in.MaxDelete < 0 || in.MaxDelete > 1000 {
		writeErr(w, http.StatusBadRequest, "bad_request", "maxOpsPerMin 1..5000, maxDelete 0..1000")
		return
	}
	if err := s.store.SetAIPolicy(r.Context(), d.ID, in); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "ai-policy", "ok", in)
	s.notifyPerm(d.ID)
	writeJSON(w, http.StatusOK, map[string]any{"aiPolicy": in})
}

// ---- content ---------------------------------------------------------------------------------------------------------

func (s *Server) getContent(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "state"
	}
	var out any
	var contentType string
	var body string
	err := s.hub.Read(r.Context(), d.ID, func(doc *model.Doc) error {
		switch format {
		case "state":
			out = map[string]any{"seq": doc.Seq, "hash": doc.Hash(), "title": d.Title, "state": doc.State()}
		case "xml":
			contentType, body = "application/xml", doc.ExportXML()
		case "svg":
			contentType, body = "image/svg+xml", doc.SVG(r.URL.Query().Get("page"))
		case "model":
			out = map[string]any{"seq": doc.Seq, "title": d.Title, "summary": doc.Summary()}
		default:
			return fmt.Errorf("unknown format %q", format)
		}
		return nil
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "unknown format") {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		s.log.Error("content", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if format == "xml" || format == "svg" {
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "export", "ok", map[string]string{"format": format})
		w.Header().Set("Content-Type", contentType)
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(d.Title)+"."+map[string]string{"xml": "drawio", "svg": "svg"}[format]+`"`)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		_, _ = w.Write([]byte(body))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

var nameRe = regexp.MustCompile(`[^A-Za-z0-9._\- ]+`)

func safeName(t string) string {
	t = strings.TrimSpace(nameRe.ReplaceAllString(t, "_"))
	if t == "" {
		return "diagram"
	}
	if len(t) > 80 {
		t = t[:80]
	}
	return t
}

// ---- operations (REST path for MCP / automation / AI; browsers use the WebSocket) ----------------------------------------

func (s *Server) postOperations(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActEdit)
	if !ok {
		return
	}
	var in struct {
		Batch string     `json:"batch"`
		Base  int64      `json:"base"`
		Ops   []model.Op `json:"ops"`
	}
	if !readJSON(w, r, &in, 8<<20) {
		return
	}
	if in.Batch == "" {
		in.Batch = "b-" + newID(8)
	}
	if len(in.Batch) > 64 || len(in.Ops) == 0 || len(in.Ops) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_batch", "batch id <= 64 chars and 1..500 ops required")
		return
	}
	if !s.aiQuota(r.Context(), d, p, len(in.Ops)) {
		writeErr(w, http.StatusTooManyRequests, "ai_rate_limited", "AI operation quota exceeded")
		return
	}
	res, err := s.Sequence(r.Context(), d.ID, d, p, in.Batch, in.Ops, in.Base, SeqAtomic)
	if err != nil {
		s.log.Error("operations", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if res.Err != nil {
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "ops", "rejected:"+res.Err.Code, map[string]any{"batch": in.Batch, "ops": len(in.Ops), "index": res.Err.Index})
		code := http.StatusUnprocessableEntity
		if res.Err.Code == "confirmation_required" {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]any{"error": map[string]any{"code": res.Err.Code, "message": res.Err.Msg, "index": res.Err.Index}})
		return
	}
	seqs := make([]int64, len(res.Applied))
	for i := range res.Applied {
		seqs[i] = res.Applied[i].Seq
	}
	if p.IsAI && !res.Duplicate {
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "ops", "ok", map[string]any{"batch": in.Batch, "ops": len(res.Applied)})
		s.touchAIPresence(d.ID, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"batch": in.Batch, "seqs": seqs, "duplicate": res.Duplicate, "overridden": res.Overridden})
}

// postLayout arranges the top-level vertices of one page ("layered" or "grid") as ordinary setPos operations, so the result
// is attributed, authorised, versioned and revertible like any other edit.
func (s *Server) postLayout(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActEdit)
	if !ok {
		return
	}
	var in struct {
		Page string `json:"page"`
		Algo string `json:"algo"`
	}
	if !readJSON(w, r, &in, 4096) {
		return
	}
	if in.Algo == "" {
		in.Algo = "layered"
	}
	if in.Algo != "layered" && in.Algo != "grid" {
		writeErr(w, http.StatusBadRequest, "bad_request", "algo must be layered or grid")
		return
	}
	var ops []model.Op
	err := s.hub.Read(r.Context(), d.ID, func(doc *model.Doc) error {
		page := in.Page
		if page == "" {
			for _, pg := range doc.SortedPages() {
				page = pg.ID
				break
			}
		}
		var err error
		ops, err = doc.LayoutOps(page, in.Algo, "l"+newID(6))
		return err
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if len(ops) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"moved": 0})
		return
	}
	if !s.aiQuota(r.Context(), d, p, len(ops)) {
		writeErr(w, http.StatusTooManyRequests, "ai_rate_limited", "AI operation quota exceeded")
		return
	}
	res, err := s.Sequence(r.Context(), d.ID, d, p, "lay-"+newID(8), ops, 0, SeqBestEffort)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if res.Err != nil && len(res.Applied) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, res.Err.Code, res.Err.Msg)
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "layout", "ok", map[string]any{"algo": in.Algo, "moved": len(res.Applied)})
	s.touchAIPresence(d.ID, p)
	writeJSON(w, http.StatusOK, map[string]any{"moved": len(res.Applied)})
}

// ---- history / revert ------------------------------------------------------------------------------------------------------

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	items, err := s.store.History(r.Context(), d.ID, limit, before)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": items})
}

func (s *Server) postRevert(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActRevert)
	if !ok {
		return
	}
	var in struct {
		Batch string `json:"batch"`
	}
	if !readJSON(w, r, &in, 4096) || in.Batch == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "batch required")
		return
	}
	batchOps, err := s.store.LoadBatch(r.Context(), d.ID, in.Batch)
	if err != nil || len(batchOps) == 0 {
		writeErr(w, http.StatusNotFound, "not_found", "batch not found")
		return
	}
	for _, o := range batchOps {
		if o.T == "restore" {
			writeErr(w, http.StatusUnprocessableEntity, "not_revertable", "a restore cannot be reverted; restore another version instead")
			return
		}
	}
	var inv []model.Op
	var notInv []string
	if err := s.hub.Read(r.Context(), d.ID, func(doc *model.Doc) error {
		inv, notInv = model.InverseOps(doc, batchOps, "rv-"+newID(4))
		return nil
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if len(inv) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "not_revertable", "nothing to revert")
		return
	}
	res, err := s.Sequence(r.Context(), d.ID, d, p, "revert-"+newID(8), inv, 0, SeqBestEffort)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	n := 0
	if res != nil {
		n = len(res.Applied)
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "revert", "ok", map[string]any{"batch": in.Batch, "applied": n, "skipped": len(res.Skipped) + len(notInv)})
	writeJSON(w, http.StatusOK, map[string]any{"reverted": in.Batch, "applied": n, "skipped": len(res.Skipped) + len(notInv)})
}

// ---- versions ----------------------------------------------------------------------------------------------------------------

func (s *Server) getVersions(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	vs, err := s.store.Versions(r.Context(), d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": vs})
}

func (s *Server) postVersion(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActVersions)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in, 4096) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 120 {
		writeErr(w, http.StatusBadRequest, "bad_request", "name 1..120 characters")
		return
	}
	var raw []byte
	var seq int64
	if err := s.hub.Read(r.Context(), d.ID, func(doc *model.Doc) error {
		raw, _ = json.Marshal(doc)
		seq = doc.Seq
		return nil
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	v := Version{ID: "v" + newID(7), Name: name, Seq: seq, CreatedBy: p.Username}
	if _, err := s.blob.Put(r.Context(), versionKey(d.ID, v.ID), raw); err != nil {
		writeErr(w, http.StatusBadGateway, "storage", "object store unavailable")
		return
	}
	if err := s.store.AddVersion(r.Context(), v, d.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "version", "ok", map[string]any{"name": name, "seq": seq})
	writeJSON(w, http.StatusCreated, map[string]any{"version": v})
}

func (s *Server) postRestore(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActRestore)
	if !ok {
		return
	}
	v, err := s.store.GetVersion(r.Context(), d.ID, r.PathValue("vid"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "version not found")
		return
	}
	raw, err := s.blob.Get(r.Context(), versionKey(d.ID, v.ID))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "storage", "version content unavailable")
		return
	}
	old := model.NewDoc()
	if err := json.Unmarshal(raw, old); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	seq, err := s.restoreTo(r.Context(), d.ID, p, v.ID, old)
	if err != nil {
		s.log.Error("restore", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "restore", "ok", map[string]any{"version": v.ID, "name": v.Name, "seq": seq})
	writeJSON(w, http.StatusOK, map[string]any{"restored": v.ID, "seq": seq})
}

// ---- comments / audit / users --------------------------------------------------------------------------------------------------

func (s *Server) getComments(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	cs, err := s.store.Comments(r.Context(), d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": cs})
}

func (s *Server) postComment(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActComment)
	if !ok {
		return
	}
	var in struct {
		Page string `json:"page"`
		Cell string `json:"cell"`
		Body string `json:"body"`
	}
	if !readJSON(w, r, &in, 16<<10) {
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" || len(in.Body) > 4000 || len(in.Page) > 128 || len(in.Cell) > 128 {
		writeErr(w, http.StatusBadRequest, "bad_request", "body 1..4000 characters")
		return
	}
	c := Comment{ID: "c" + newID(8), Page: in.Page, Cell: in.Cell, Author: p.Username, AuthorType: p.Actor.Type, Body: in.Body}
	if err := s.store.AddComment(r.Context(), d.ID, c); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "comment", "ok", map[string]string{"id": c.ID})
	writeJSON(w, http.StatusCreated, map[string]any{"comment": c})
}

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActAudit)
	if !ok {
		return
	}
	ev, err := s.store.AuditFor(r.Context(), d.ID, 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev})
}

func (s *Server) searchUsers(w http.ResponseWriter, r *http.Request, p *Principal) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 64 {
		q = q[:64]
	}
	if p.IsAI {
		writeErr(w, http.StatusForbidden, "forbidden", "not available to AI actors")
		return
	}
	us, err := s.store.SearchUsers(r.Context(), q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": us})
}

var _ = identity.ErrInvalid

func (s *Server) createSnapshotRow(ctx context.Context, docID, key string, raw []byte) error {
	sum := sha256.Sum256(raw)
	return s.store.AddSnapshot(ctx, docID, 0, key, hex.EncodeToString(sum[:]), len(raw))
}
