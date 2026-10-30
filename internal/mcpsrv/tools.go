package mcpsrv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/model"
)

var errUnknownTool = errors.New("unknown tool")

// toolError is a failure the agent should see and may react to (not a server fault).
type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

func terr(format string, a ...any) error { return &toolError{fmt.Sprintf(format, a...)} }

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// ---- platform client -----------------------------------------------------------------------------------------------

// call performs one platform request as the AI actor (a fresh 1-minute internal token per call, ADR-0004/0009).
func (s *Server) call(ctx context.Context, a actor, method, path string, body any) (int, []byte, error) {
	tok, err := s.cfg.Signer.Sign(identity.Claims{Subject: a.Name, Username: a.Name, Name: a.Display, Audience: "platform", ActorType: identity.ActorAI,
		OnBehalfOf: a.OnBehalfOf, Agent: a.Agent, Model: a.Model, Roles: []string{s.cfg.RequiredRole}, SessionID: newID("m"),
		TokenID: a.TokenID, DocScope: a.DocScope, DocRole: a.DocRole}, time.Minute)
	if err != nil {
		return 0, nil, err
	}
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.cfg.PlatformURL, "/")+"/api/v1"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return resp.StatusCode, out, err
}

// platformError turns a non-2xx platform answer into something the agent can act on.
func platformError(status int, body []byte) error {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Index   int    `json:"index"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	switch {
	case status == http.StatusNotFound:
		return terr("not found: the diagram does not exist, is not shared with you, or does not allow AI collaborators")
	case status == http.StatusForbidden:
		return terr("forbidden: your role on this diagram does not allow this action (%s)", e.Error.Code)
	case status == http.StatusConflict && e.Error.Code == "confirmation_required":
		return terr("confirmation required: %s. Ask the diagram owner to raise the AI deletion limit in Share > AI collaborators, or delete fewer cells per request", e.Error.Message)
	case status == http.StatusTooManyRequests:
		return terr("rate limited: the diagram's AI operation quota for this minute is used up; retry later with fewer operations")
	case status == http.StatusUnprocessableEntity:
		return terr("operation rejected (%s, op #%d): %s", e.Error.Code, e.Error.Index, e.Error.Message)
	case status >= 400 && status < 500:
		return terr("request refused (%d %s): %s", status, e.Error.Code, e.Error.Message)
	}
	return fmt.Errorf("platform answered %d", status)
}

func (s *Server) get(ctx context.Context, a actor, path string, v any) error {
	st, b, err := s.call(ctx, a, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if st/100 != 2 {
		return platformError(st, b)
	}
	if v != nil {
		return json.Unmarshal(b, v)
	}
	return nil
}

func (s *Server) post(ctx context.Context, a actor, path string, in, out any) error {
	st, b, err := s.call(ctx, a, http.MethodPost, path, in)
	if err != nil {
		return err
	}
	if st/100 != 2 {
		return platformError(st, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// ---- tool catalogue ------------------------------------------------------------------------------------------------

// obj builds a JSON Schema object. "required" is left out when empty: a nil slice would be sent as null, which strict MCP
// clients (the official SDK validates tools/list with zod) reject, and they then drop the whole tool list.
func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	pStr  = func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	pNum  = func(d string) map[string]any { return map[string]any{"type": "number", "description": d} }
	docID = pStr("Diagram id (from list_diagrams)")
	pBool = func(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }
	pEnum = func(d string, vals ...string) map[string]any {
		return map[string]any{"type": "string", "enum": vals, "description": d}
	}
	pArr = func(d string, item map[string]any) map[string]any {
		return map[string]any{"type": "array", "description": d, "items": item}
	}
)

// toolDefsFor is the catalogue an actor sees: a per-diagram token cannot create diagrams and need not name the diagram.
func toolDefsFor(ac actor) []map[string]any {
	defs := toolDefs()
	if ac.DocScope == "" {
		return defs
	}
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		if d["name"] == "create_diagram" {
			continue
		}
		schema, _ := d["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		req, _ := schema["required"].([]string)
		if _, has := props["document"]; has {
			np := map[string]any{}
			for k, v := range props {
				np[k] = v
			}
			np["document"] = pStr("Diagram id (optional: this token opens exactly one diagram, " + ac.DocScope + ")")
			nreq := []string{}
			for _, r := range req {
				if r != "document" {
					nreq = append(nreq, r)
				}
			}
			ns := map[string]any{}
			for k, v := range schema {
				ns[k] = v
			}
			ns["properties"] = np
			if len(nreq) > 0 {
				ns["required"] = nreq
			} else {
				delete(ns, "required")
			}
			nd := map[string]any{}
			for k, v := range d {
				nd[k] = v
			}
			nd["inputSchema"] = ns
			d = nd
		}
		out = append(out, d)
	}
	return out
}

func toolDefs() []map[string]any {
	t := func(name, desc string, schema map[string]any, readOnly bool) map[string]any {
		return map[string]any{"name": name, "description": desc, "inputSchema": schema, "annotations": map[string]any{"readOnlyHint": readOnly}}
	}
	return []map[string]any{
		t("list_diagrams", "List the diagrams you can work on (shared with you and allowing AI collaborators).", obj(map[string]any{}), true),
		t("get_diagram", "Read a diagram as a structured summary (pages, shapes with ids/labels/positions, edges). Use format=svg for an image or xml for the .drawio file.",
			obj(map[string]any{"document": docID, "format": map[string]any{"type": "string", "enum": []string{"summary", "svg", "xml"}, "description": "default summary"}}, "document"), true),
		t("create_diagram", "Create a new diagram owned by the signed-in user and open to AI collaboration. Optionally start from draw.io XML.",
			obj(map[string]any{"title": pStr("Title"), "xml": pStr("Optional .drawio / mxGraph XML")}, "title"), false),
		t("add_shape", "Add ONE shape to a diagram. Returns its cell id. For more than a few shapes use build_diagram instead (it lays everything out for you). Without a style the shape gets a readable default; x/y are only needed to place it next to existing shapes (see get_diagram for positions).",
			obj(map[string]any{"document": docID, "label": pStr("Text shown in the shape"), "x": pNum("x position"), "y": pNum("y position"), "width": pNum("default 120"), "height": pNum("default 60"),
				"style": pStr("draw.io style string, e.g. rounded=1;fillColor=#dae8fc;strokeColor=#6c8ebf;"), "page": pStr("Page id (default: first page)"), "cell": pStr("Optional cell id to use")}, "document", "label"), false),
		t("connect", "Connect two existing cells with an edge. Returns the edge id. Both cells must exist. Without a style the edge is an orthogonal arrow; run layout (algo=hierarchical) afterwards to route it around shapes.",
			obj(map[string]any{"document": docID, "source": pStr("Source cell id"), "target": pStr("Target cell id"), "label": pStr("Edge label"), "style": pStr("draw.io style"), "page": pStr("Page id")}, "document", "source", "target"), false),
		t("update_cell", "Change the label, style or geometry of an existing cell.",
			obj(map[string]any{"document": docID, "cell": pStr("Cell id"), "label": pStr("New label"), "style": pStr("Style keys to set, e.g. fillColor=#f8cecc;rounded=1"), "x": pNum("New x"), "y": pNum("New y"), "width": pNum("New width"), "height": pNum("New height"), "page": pStr("Page id")}, "document", "cell"), false),
		t("delete_cells", "Delete cells (and what hangs off them). Large deletions need the owner's confirmation (diagram AI policy).",
			obj(map[string]any{"document": docID, "cells": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "page": pStr("Page id")}, "document", "cells"), false),
		t("apply_operations", "Low-level: apply semantic operations (addCell, deleteCell, setProp, setPos, connect, reparent, reorder, addPage, deletePage, renamePage, reorderPage, setPageAttrs) atomically. Operation ids are generated when omitted.",
			obj(map[string]any{"document": docID, "operations": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}, "document", "operations"), false),
		t("build_diagram", "Create shapes, containers and connections from a declarative description and let the server do ALL the geometry: shapes are sized from their labels, containers are fitted around their members, shapes never overlap, and edges are routed through the gaps. Preferred way to draw anything beyond 3 shapes. Everything is added in one atomic step (all or nothing) to the page; ids must be new. Returns what was created plus a lint report. Up to 480 groups+nodes+edges per call.",
			obj(map[string]any{"document": docID, "page": pStr("Page id (default: first page)"),
				"direction": pEnum("Flow direction: TB top-to-bottom (default), LR left-to-right", "TB", "LR"),
				"groups": pArr("Containers (a boundary, tier, namespace). May nest via parent.", obj(map[string]any{"id": pStr("Unique id: letters, digits, _ . : -"), "label": pStr("Title shown at the top"),
					"parent": pStr("Id of an enclosing group"), "color": pEnum("Tint", model.Colors()...)}, "id", "label")),
				"nodes": pArr("Shapes.", obj(map[string]any{"id": pStr("Unique id: letters, digits, _ . : -"), "label": pStr("Text (2-4 words; \\n for a second line)"),
					"group": pStr("Id of the group that contains it"), "kind": pEnum("Shape kind (default box)", model.Kinds()...), "color": pEnum("Fill color (default blue)", model.Colors()...),
					"width": pNum("Optional; default follows the label"), "height": pNum("Optional; default follows the label")}, "id", "label")),
				"edges": pArr("Connections between node or group ids.", obj(map[string]any{"from": pStr("Source id"), "to": pStr("Target id"), "label": pStr("Optional, short"), "dashed": pBool("Dashed line for async/optional")}, "from", "to"))},
				"document", "nodes"), false),
		t("lint_diagram", "Check a diagram the way a person would look at it: overlapping shapes, shapes sticking out of their container, labels that do not fit, edges running through shapes or labels, edge crossings, dangling edges. Each finding names the cells and a fix. Read-only; call it after building or editing.",
			obj(map[string]any{"document": docID, "page": pStr("Page id (default: first page)")}, "document"), true),
		t("layout", "Re-arrange a whole page automatically. algo=hierarchical (default): layers by edges, containers fitted around their members, edges routed around shapes, fewer crossings; hierarchical-lr flows left to right; layered/grid only move top-level shapes. Fixes most lint findings. Only values that change cost an operation.",
			obj(map[string]any{"document": docID, "algo": pEnum("Algorithm (default hierarchical)", "hierarchical", "hierarchical-lr", "layered", "grid"), "page": pStr("Page id")}, "document"), false),
		t("export", "Export a diagram as svg (first page) or xml (.drawio).", obj(map[string]any{"document": docID, "format": map[string]any{"type": "string", "enum": []string{"svg", "xml"}}}, "document", "format"), true),
		t("get_history", "Recent changes with who made them (people and AI agents).", obj(map[string]any{"document": docID, "limit": pNum("default 20")}, "document"), true),
		t("list_versions", "Named versions of a diagram.", obj(map[string]any{"document": docID}, "document"), true),
		t("save_version", "Save the current state as a named version.", obj(map[string]any{"document": docID, "name": pStr("Version name")}, "document", "name"), false),
		t("restore_version", "Restore a named version (the current state stays in history).", obj(map[string]any{"document": docID, "version": pStr("Version id from list_versions")}, "document", "version"), false),
		t("comment", "Add a comment to the diagram (optionally about one cell).", obj(map[string]any{"document": docID, "body": pStr("Comment text"), "cell": pStr("Cell id"), "page": pStr("Page id")}, "document", "body"), false),
	}
}

type docState struct {
	Seq   int64  `json:"seq"`
	Title string `json:"title"`
	State []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Cells []struct {
			ID     string            `json:"id"`
			Kind   string            `json:"k"`
			Parent string            `json:"p"`
			Order  string            `json:"o"`
			Props  map[string]string `json:"props"`
		} `json:"cells"`
	} `json:"state"`
}

// pageAndLayer resolves the default page and its first layer (the parent of ordinary shapes).
func (s *Server) pageAndLayer(ctx context.Context, a actor, doc, page string) (string, string, *docState, error) {
	var st docState
	if err := s.get(ctx, a, "/documents/"+url.PathEscape(doc)+"/content?format=state", &st); err != nil {
		return "", "", nil, err
	}
	for _, p := range st.State {
		if page != "" && p.ID != page {
			continue
		}
		for _, c := range p.Cells {
			if c.Parent == "0" {
				return p.ID, c.ID, &st, nil
			}
		}
	}
	return "", "", nil, terr("page %q not found", page)
}

func parseStyle(style string) map[string]string {
	props := map[string]string{}
	var names []string
	for _, tok := range strings.Split(style, ";") {
		if tok == "" {
			continue
		}
		if i := strings.IndexByte(tok, '='); i > 0 {
			props["st:"+tok[:i]] = tok[i+1:]
		} else {
			names = append(names, tok)
		}
	}
	if len(names) > 0 {
		props["sn"] = strings.Join(names, ";")
	}
	return props
}

func num(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
}

func (s *Server) applyOps(ctx context.Context, a actor, doc string, ops []map[string]any) ([]int64, error) {
	for _, o := range ops {
		if _, ok := o["id"]; !ok {
			o["id"] = newID("o")
		}
	}
	var out struct {
		Seqs []int64 `json:"seqs"`
	}
	if err := s.post(ctx, a, "/documents/"+url.PathEscape(doc)+"/operations", map[string]any{"batch": newID("b"), "ops": ops}, &out); err != nil {
		return nil, err
	}
	return out.Seqs, nil
}

type args map[string]any

func (a args) str(k string) string { v, _ := a[k].(string); return v }
func (a args) f(k string, def float64) float64 {
	if v, ok := a[k].(float64); ok {
		return v
	}
	return def
}
func (a args) has(k string) bool { _, ok := a[k]; return ok }

func pretty(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) }

func (s *Server) callTool(ctx context.Context, ac actor, name string, raw json.RawMessage) (string, error) {
	a := args{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", terr("arguments must be a JSON object")
		}
	}
	if ac.DocScope != "" { // a per-diagram token: this diagram only, "document" may be left out
		if name == "create_diagram" {
			return "", terr("this token is bound to one diagram and cannot create new ones")
		}
		if d := a.str("document"); d != "" && d != ac.DocScope {
			return "", terr("this token only gives access to diagram %s", ac.DocScope)
		}
		a["document"] = ac.DocScope
	}
	doc := a.str("document")
	needDoc := func() error {
		if doc == "" {
			return terr("document is required")
		}
		return nil
	}
	switch name {
	case "list_diagrams":
		var r struct {
			Documents []struct {
				ID, Title, Owner string
				HeadSeq          int64 `json:"headSeq"`
				Role             string
			} `json:"documents"`
		}
		if err := s.get(ctx, ac, "/documents", &r); err != nil {
			return "", err
		}
		if len(r.Documents) == 0 {
			return "No diagrams are open to you. Ask the diagram owner to enable AI collaborators and share the diagram with \"" + ac.Name + "\".", nil
		}
		var b strings.Builder
		for _, d := range r.Documents {
			fmt.Fprintf(&b, "- %s  %q  owner=%s  your role=%s  revision=%d\n", d.ID, d.Title, d.Owner, d.Role, d.HeadSeq)
		}
		return b.String(), nil
	case "get_diagram":
		if err := needDoc(); err != nil {
			return "", err
		}
		switch a.str("format") {
		case "svg", "xml":
			return s.export(ctx, ac, doc, a.str("format"))
		}
		var r map[string]any
		if err := s.get(ctx, ac, "/documents/"+url.PathEscape(doc)+"/content?format=model", &r); err != nil {
			return "", err
		}
		return pretty(r), nil
	case "export":
		if err := needDoc(); err != nil {
			return "", err
		}
		return s.export(ctx, ac, doc, a.str("format"))
	case "create_diagram":
		var r struct {
			Document struct{ ID, Title string } `json:"document"`
		}
		if err := s.post(ctx, ac, "/documents", map[string]any{"title": a.str("title"), "xml": a.str("xml")}, &r); err != nil {
			return "", err
		}
		return fmt.Sprintf("Created diagram %s (%q). Add shapes with add_shape / connect.", r.Document.ID, r.Document.Title), nil
	case "add_shape":
		if err := needDoc(); err != nil {
			return "", err
		}
		page, layer, _, err := s.pageAndLayer(ctx, ac, doc, a.str("page"))
		if err != nil {
			return "", err
		}
		id := a.str("cell")
		if id == "" {
			id = newID("s")
		}
		style, label := a.str("style"), a.str("label")
		if style == "" { // a readable default: the label is shown literally, so it must be escaped for html=1
			style = "rounded=1;arcSize=12;whiteSpace=wrap;html=1;fontSize=12;fontColor=#1f2937;fillColor=#dae8fc;strokeColor=#6c8ebf;strokeWidth=1.5"
			label = model.HTMLLabel(label)
		}
		props := parseStyle(style)
		props["v"], props["gx"], props["gy"], props["gw"], props["gh"] = label, num(clamp(a.f("x", 40), -1e5, 1e5)), num(clamp(a.f("y", 40), -1e5, 1e5)), num(clamp(a.f("width", 120), 10, 2000)), num(clamp(a.f("height", 60), 10, 2000))
		if _, err := s.applyOps(ctx, ac, doc, []map[string]any{{"t": "addCell", "page": page, "cell": id, "kind": "v", "parent": layer, "props": props}}); err != nil {
			return "", err
		}
		return "Added shape " + id + ". Tip: for several shapes use build_diagram, then lint_diagram.", nil
	case "connect":
		if err := needDoc(); err != nil {
			return "", err
		}
		page, layer, st, err := s.pageAndLayer(ctx, ac, doc, a.str("page"))
		if err != nil {
			return "", err
		}
		var missing []string
		for _, end := range []string{a.str("source"), a.str("target")} {
			if !st.hasCell(page, end) {
				missing = append(missing, fmt.Sprintf("%q", end))
			}
		}
		if len(missing) > 0 { // never create an edge that points at nothing (it would be silently invisible)
			return "", terr("cell %s not found on page %s: read the diagram (get_diagram) for the real ids", strings.Join(missing, " and "), page)
		}
		id := newID("e")
		style := a.str("style")
		if style == "" {
			style = "edgeStyle=orthogonalEdgeStyle;rounded=1;orthogonalLoop=1;jettySize=auto;html=1;endArrow=block;endFill=1;strokeWidth=1.5;strokeColor=#6b7280;fontSize=11;fontColor=#374151;labelBackgroundColor=default"
		}
		props := parseStyle(style)
		props["src"], props["tgt"] = a.str("source"), a.str("target")
		props["gx"], props["gy"], props["gw"], props["gh"], props["grel"] = "0", "0", "0", "0", "1"
		if l := a.str("label"); l != "" {
			props["v"] = model.HTMLLabel(l)
		}
		if _, err := s.applyOps(ctx, ac, doc, []map[string]any{{"t": "addCell", "page": page, "cell": id, "kind": "e", "parent": layer, "props": props}}); err != nil {
			return "", err
		}
		return "Added edge " + id, nil
	case "update_cell":
		if err := needDoc(); err != nil {
			return "", err
		}
		page, _, _, err := s.pageAndLayer(ctx, ac, doc, a.str("page"))
		if err != nil {
			return "", err
		}
		cell := a.str("cell")
		var ops []map[string]any
		set := func(k, v string) {
			ops = append(ops, map[string]any{"t": "setProp", "page": page, "cell": cell, "key": k, "val": v})
		}
		if a.has("label") {
			set("v", a.str("label"))
		}
		if st := a.str("style"); st != "" {
			for k, v := range parseStyle(st) {
				set(k, v)
			}
		}
		if a.has("x") || a.has("y") {
			ops = append(ops, map[string]any{"t": "setPos", "page": page, "cell": cell, "x": a.f("x", 0), "y": a.f("y", 0)})
		}
		if a.has("width") {
			set("gw", num(a.f("width", 120)))
		}
		if a.has("height") {
			set("gh", num(a.f("height", 60)))
		}
		if len(ops) == 0 {
			return "", terr("nothing to change: pass label, style, x/y or width/height")
		}
		if _, err := s.applyOps(ctx, ac, doc, ops); err != nil {
			return "", err
		}
		return "Updated " + cell, nil
	case "delete_cells":
		if err := needDoc(); err != nil {
			return "", err
		}
		page, _, _, err := s.pageAndLayer(ctx, ac, doc, a.str("page"))
		if err != nil {
			return "", err
		}
		list, _ := a["cells"].([]any)
		var ops []map[string]any
		for _, c := range list {
			if id, ok := c.(string); ok {
				ops = append(ops, map[string]any{"t": "deleteCell", "page": page, "cell": id})
			}
		}
		if len(ops) == 0 {
			return "", terr("cells must list at least one cell id")
		}
		if _, err := s.applyOps(ctx, ac, doc, ops); err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted %d cell(s) (and their children)", len(ops)), nil
	case "apply_operations":
		if err := needDoc(); err != nil {
			return "", err
		}
		list, _ := a["operations"].([]any)
		var ops []map[string]any
		for _, o := range list {
			if m, ok := o.(map[string]any); ok {
				ops = append(ops, m)
			}
		}
		if len(ops) == 0 {
			return "", terr("operations must be a non-empty array")
		}
		seqs, err := s.applyOps(ctx, ac, doc, ops)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Applied %d operation(s), revisions %v", len(seqs), seqs), nil
	case "layout":
		if err := needDoc(); err != nil {
			return "", err
		}
		var r struct {
			Moved int `json:"moved"`
		}
		if err := s.post(ctx, ac, "/documents/"+url.PathEscape(doc)+"/layout", map[string]any{"page": a.str("page"), "algo": a.str("algo")}, &r); err != nil {
			return "", err
		}
		return fmt.Sprintf("Layout changed %d value(s). Run lint_diagram to confirm the result.", r.Moved), nil
	case "build_diagram":
		if err := needDoc(); err != nil {
			return "", err
		}
		body := map[string]any{}
		for _, k := range []string{"page", "direction", "groups", "nodes", "edges"} {
			if v, ok := a[k]; ok {
				body[k] = v
			}
		}
		var r struct {
			Page string            `json:"page"`
			Lint *model.LintReport `json:"lint"`
			G    int               `json:"groups"`
			N    int               `json:"nodes"`
			E    int               `json:"edges"`
			W    float64           `json:"width"`
			H    float64           `json:"height"`
		}
		if err := s.post(ctx, ac, "/documents/"+url.PathEscape(doc)+"/build", body, &r); err != nil {
			return "", err
		}
		out := fmt.Sprintf("Built %d shape(s), %d edge(s), %d group(s) on page %s, about %.0f x %.0f px (one revertible step).\n", r.N, r.E, r.G, r.Page, r.W, r.H)
		return out + formatLint(r.Lint), nil
	case "lint_diagram":
		if err := needDoc(); err != nil {
			return "", err
		}
		var rep model.LintReport
		if err := s.get(ctx, ac, "/documents/"+url.PathEscape(doc)+"/lint?page="+url.QueryEscape(a.str("page")), &rep); err != nil {
			return "", err
		}
		return formatLint(&rep), nil
	case "get_history":
		if err := needDoc(); err != nil {
			return "", err
		}
		var r map[string]any
		if err := s.get(ctx, ac, fmt.Sprintf("/documents/%s/history?limit=%d", url.PathEscape(doc), int(a.f("limit", 20))), &r); err != nil {
			return "", err
		}
		return pretty(r["history"]), nil
	case "list_versions":
		if err := needDoc(); err != nil {
			return "", err
		}
		var r map[string]any
		if err := s.get(ctx, ac, "/documents/"+url.PathEscape(doc)+"/versions", &r); err != nil {
			return "", err
		}
		return pretty(r["versions"]), nil
	case "save_version":
		if err := needDoc(); err != nil {
			return "", err
		}
		var r map[string]any
		if err := s.post(ctx, ac, "/documents/"+url.PathEscape(doc)+"/versions", map[string]any{"name": a.str("name")}, &r); err != nil {
			return "", err
		}
		return "Saved version: " + pretty(r["version"]), nil
	case "restore_version":
		if err := needDoc(); err != nil {
			return "", err
		}
		if err := s.post(ctx, ac, "/documents/"+url.PathEscape(doc)+"/versions/"+url.PathEscape(a.str("version"))+"/restore", map[string]any{}, nil); err != nil {
			return "", err
		}
		return "Version restored", nil
	case "comment":
		if err := needDoc(); err != nil {
			return "", err
		}
		if err := s.post(ctx, ac, "/documents/"+url.PathEscape(doc)+"/comments", map[string]any{"body": a.str("body"), "cell": a.str("cell"), "page": a.str("page")}, nil); err != nil {
			return "", err
		}
		return "Comment added", nil
	}
	return "", errUnknownTool
}

func (s *Server) export(ctx context.Context, a actor, doc, format string) (string, error) {
	if format != "svg" && format != "xml" {
		return "", terr("format must be svg or xml")
	}
	st, b, err := s.call(ctx, a, http.MethodGet, "/documents/"+url.PathEscape(doc)+"/content?format="+format, nil)
	if err != nil {
		return "", err
	}
	if st/100 != 2 {
		return "", platformError(st, b)
	}
	return string(b), nil
}

// ---- resources: diagram://{id} --------------------------------------------------------------------------------------

func (s *Server) listResources(ctx context.Context, a actor) (any, *rpcErr) {
	var r struct {
		Documents []struct{ ID, Title string } `json:"documents"`
	}
	if err := s.get(ctx, a, "/documents", &r); err != nil {
		return map[string]any{"resources": []any{}}, nil
	}
	res := []map[string]any{}
	for _, d := range r.Documents {
		res = append(res, map[string]any{"uri": "diagram://" + d.ID, "name": d.Title, "mimeType": "application/json", "description": "Structured summary of the diagram"})
	}
	return map[string]any{"resources": res}, nil
}

func (s *Server) readResource(ctx context.Context, a actor, params json.RawMessage) (any, *rpcErr) {
	var in struct {
		URI string `json:"uri"`
	}
	if json.Unmarshal(params, &in) != nil || !strings.HasPrefix(in.URI, "diagram://") {
		return nil, &rpcErr{Code: -32602, Message: "uri must be diagram://<id>"}
	}
	id := strings.TrimPrefix(in.URI, "diagram://")
	var r map[string]any
	if err := s.get(ctx, a, "/documents/"+url.PathEscape(id)+"/content?format=model", &r); err != nil {
		return nil, &rpcErr{Code: -32002, Message: err.Error()}
	}
	return map[string]any{"contents": []map[string]any{{"uri": in.URI, "mimeType": "application/json", "text": pretty(r)}}}, nil
}

func clamp(f, lo, hi float64) float64 {
	if f < lo {
		return lo
	}
	if f > hi {
		return hi
	}
	return f
}

// hasCell reports whether the page of the state read from the platform has a cell with this id.
func (d *docState) hasCell(page, id string) bool {
	if d == nil || id == "" {
		return false
	}
	for _, p := range d.State {
		if p.ID != page {
			continue
		}
		for _, c := range p.Cells {
			if c.ID == id {
				return true
			}
		}
	}
	return false
}

// formatLint renders a lint report for an agent: a verdict first, then every finding with its fix.
func formatLint(r *model.LintReport) string {
	if r == nil {
		return "Lint unavailable."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Lint: %d shape(s), %d edge(s), %d edge crossing(s). ", r.Shapes, r.Edges, r.Crossings)
	if len(r.Issues) == 0 {
		b.WriteString("No problems found.")
		return b.String()
	}
	if r.Clean() {
		b.WriteString("No problems, only notes:\n")
	} else {
		b.WriteString("Problems to fix:\n")
	}
	for i, is := range r.Issues {
		if i >= 25 {
			fmt.Fprintf(&b, "... and %d more\n", len(r.Issues)-i)
			break
		}
		fmt.Fprintf(&b, "- [%s] %s: %s", is.Severity, is.Code, is.Msg)
		if is.Fix != "" {
			b.WriteString(" -> " + is.Fix)
		}
		b.WriteString("\n")
	}
	return b.String()
}
