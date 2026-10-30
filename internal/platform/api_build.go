package platform

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Glhby13/drawio-custom-service/internal/model"
)

// firstPageAndLayer picks the page (the requested one, else the first) and its first layer: the parent of ordinary shapes.
func firstPageAndLayer(doc *model.Doc, page string) (string, string, *model.Page, bool) {
	if page == "" {
		for _, pg := range doc.SortedPages() {
			page = pg.ID
			break
		}
	}
	pg := doc.Pages[page]
	if pg == nil || pg.Dead != 0 {
		return "", "", nil, false
	}
	for _, c := range pg.Children("0") {
		return page, c.ID, pg, true
	}
	return "", "", nil, false
}

// postBuild creates a whole diagram part from a declarative description (groups, nodes, edges). The server sizes the shapes from
// their labels and lays everything out (model.AutoLayout), so a caller that cannot see the canvas still gets a tidy result. All
// cells go in as ONE atomic batch: either the whole structure appears or nothing does.
func (s *Server) postBuild(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActEdit)
	if !ok {
		return
	}
	var in struct {
		Page string `json:"page"`
		model.BuildSpec
	}
	if !readJSON(w, r, &in, 1<<20) {
		return
	}
	var ops []model.Op
	var rep *model.BuildReport
	var page string
	err := s.hub.Read(r.Context(), d.ID, func(doc *model.Doc) error {
		pageID, layer, pg, found := firstPageAndLayer(doc, in.Page)
		if !found {
			return errors.New("page not found")
		}
		page = pageID
		existing := make(map[string]bool, len(pg.Cells))
		for id := range pg.Cells {
			existing[id] = true
		}
		var err error
		ops, rep, err = model.BuildOps(&in.BuildSpec, pageID, layer, existing, "b"+newID(5))
		return err
	})
	if err != nil {
		var be *model.BuildError
		if errors.As(err, &be) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "bad_spec", "message": be.Error(), "issues": be.Issues}})
			return
		}
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !s.aiQuota(r.Context(), d, p, len(ops)) {
		writeErr(w, http.StatusTooManyRequests, "ai_rate_limited", "AI operation quota exceeded: this build needs "+strconv.Itoa(len(ops))+" operations; raise 'Max AI operations per minute' in the diagram's AI settings or build in smaller parts")
		return
	}
	batch := "bld-" + newID(8)
	res, err := s.Sequence(r.Context(), d.ID, d, p, batch, ops, 0, SeqAtomic)
	if err != nil {
		s.log.Error("build", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if res.Err != nil {
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "build", "rejected:"+res.Err.Code, map[string]any{"batch": batch, "ops": len(ops)})
		code := http.StatusUnprocessableEntity
		if res.Err.Code == "confirmation_required" {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]any{"error": map[string]any{"code": res.Err.Code, "message": res.Err.Msg, "index": res.Err.Index}})
		return
	}
	if p.IsAI && !res.Duplicate {
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "build", "ok", map[string]any{"batch": batch, "ops": len(res.Applied)})
		s.touchAIPresence(d.ID, p)
		s.announceAI(d.ID, p, appliedCells(res.Applied))
	}
	out := map[string]any{"batch": batch, "page": page, "groups": rep.Groups, "nodes": rep.Nodes, "edges": rep.Edges, "width": rep.Width, "height": rep.Height}
	var lint *model.LintReport
	if err := s.hub.Read(r.Context(), d.ID, func(doc *model.Doc) error {
		var err error
		lint, err = doc.Lint(page)
		return err
	}); err == nil {
		out["lint"] = lint
	}
	writeJSON(w, http.StatusOK, out)
}

// getLint reads the diagram back and reports what a person would notice: overlaps, labels that do not fit, edges through shapes.
func (s *Server) getLint(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, _, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	var rep *model.LintReport
	err := s.hub.Read(r.Context(), d.ID, func(doc *model.Doc) error {
		page, _, _, found := firstPageAndLayer(doc, r.URL.Query().Get("page"))
		if !found {
			return errors.New("page not found")
		}
		var err error
		rep, err = doc.Lint(page)
		return err
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
