package model

import (
	"fmt"
	"strings"
	"testing"
)

// archSpec is shaped like the diagram that came out untidy when a model placed every coordinate itself: nested containers, a
// fan-out gateway, shared stores, a queue with two consumers, labelled edges and a long edge that skips layers.
func archSpec() *BuildSpec {
	return &BuildSpec{
		Groups: []BuildGroup{
			{ID: "edge", Label: "Edge", Color: "gray"},
			{ID: "ns", Label: "Namespace lab-portal", Color: "green"},
			{ID: "data", Label: "Data stores", Color: "red"},
			{ID: "ext", Label: "External", Color: "purple"},
		},
		Nodes: []BuildNode{
			{ID: "browser", Label: "Browser (SPA)", Group: "edge", Kind: "actor"},
			{ID: "nginx", Label: "nginx TLS edge", Group: "edge"},
			{ID: "gw", Label: "gateway (BFF)\nsession, CSRF, rate limit", Group: "ns", Kind: "service"},
			{ID: "core", Label: "core-service", Group: "ns", Kind: "service"},
			{ID: "ticket", Label: "admin-service-ticket", Group: "ns", Kind: "service"},
			{ID: "act", Label: "activity-service", Group: "ns", Kind: "service"},
			{ID: "audit", Label: "audit-service", Group: "ns", Kind: "service"},
			{ID: "mail", Label: "email-notification-service", Group: "ns", Kind: "service"},
			{ID: "pg", Label: "PostgreSQL 18", Group: "data", Kind: "database", Color: "red"},
			{ID: "redis", Label: "Redis 8", Group: "data", Kind: "database", Color: "red"},
			{ID: "kafka", Label: "Kafka topics", Group: "data", Kind: "queue", Color: "orange"},
			{ID: "kc", Label: "Keycloak OIDC", Group: "ext", Kind: "external"},
			{ID: "smtp", Label: "SMTP (Mailpit)", Group: "ext", Kind: "external"},
		},
		Edges: []BuildEdge{
			{From: "browser", To: "nginx", Label: "HTTPS"}, {From: "nginx", To: "gw"},
			{From: "gw", To: "core"}, {From: "gw", To: "ticket"}, {From: "gw", To: "act"},
			{From: "gw", To: "redis", Label: "session"}, {From: "gw", To: "kc", Label: "OIDC", Dashed: true},
			{From: "core", To: "pg"}, {From: "ticket", To: "pg"}, {From: "act", To: "pg"},
			{From: "core", To: "kafka", Label: "outbox"}, {From: "ticket", To: "kafka"}, {From: "act", To: "kafka"},
			{From: "kafka", To: "audit"}, {From: "kafka", To: "mail"},
			{From: "audit", To: "pg"}, {From: "mail", To: "smtp", Label: "email"},
		},
	}
}

func blankPage(t *testing.T) (*Doc, string, string) {
	t.Helper()
	d, err := ImportXML(BlankXML)
	if err != nil {
		t.Fatal(err)
	}
	pg := d.SortedPages()[0]
	for _, c := range pg.Children("0") {
		return d, pg.ID, c.ID
	}
	t.Fatal("no layer")
	return nil, "", ""
}

func buildAndLint(t *testing.T, spec *BuildSpec) (*Doc, *LintReport, string) {
	t.Helper()
	d, page, layer := blankPage(t)
	ops, rep, err := BuildOps(spec, page, layer, map[string]bool{}, "t")
	if err != nil {
		t.Fatalf("BuildOps: %v", err)
	}
	if rep.Nodes != len(spec.Nodes) || len(ops) != len(spec.Groups)+len(spec.Nodes)+len(spec.Edges) {
		t.Fatalf("report/ops mismatch: %+v, %d ops", rep, len(ops))
	}
	d.FillOrders(ops)
	if _, oe := d.ApplyBatch(ops); oe != nil {
		t.Fatalf("ops rejected: %+v", oe)
	}
	lr, err := d.Lint(page)
	if err != nil {
		t.Fatal(err)
	}
	return d, lr, page
}

func dump(lr *LintReport) string {
	var b strings.Builder
	for _, i := range lr.Issues {
		fmt.Fprintf(&b, "\n  [%s] %s: %s", i.Severity, i.Code, i.Msg)
	}
	return b.String()
}

func TestBuildProducesCleanDiagram(t *testing.T) {
	for _, dir := range []string{"TB", "LR"} {
		spec := archSpec()
		spec.Direction = dir
		_, lr, _ := buildAndLint(t, spec)
		if lr.Shapes != 17 || lr.Edges != 17 {
			t.Fatalf("%s: shapes=%d edges=%d", dir, lr.Shapes, lr.Edges)
		}
		for _, i := range lr.Issues {
			if i.Code == "overlap" || i.Code == "outside_container" || i.Code == "label_overflow" || i.Code == "dangling_edge" || i.Code == "edge_label_on_shape" || i.Code == "edge_through_shape" {
				t.Errorf("%s: %s", dir, dump(lr))
				break
			}
		}
		if lr.Crossings > 14 { // a dense 17-edge graph still needs a few; the blind placement this replaces had 20+ and edges through shapes
			t.Errorf("%s: %d edge crossings: %s", dir, lr.Crossings, dump(lr))
		}
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a, _, _ := buildAndLint(t, archSpec())
	b, _, _ := buildAndLint(t, archSpec())
	if a.Hash() != b.Hash() {
		t.Fatal("same spec produced different diagrams")
	}
}

func TestBuildValidation(t *testing.T) {
	bad := []struct {
		name string
		mut  func(*BuildSpec)
		want string
	}{
		{"unknown kind", func(s *BuildSpec) { s.Nodes[0].Kind = "blob" }, "kind"},
		{"unknown color", func(s *BuildSpec) { s.Nodes[0].Color = "pink" }, "color"},
		{"dup id", func(s *BuildSpec) { s.Nodes[1].ID = s.Nodes[0].ID }, "duplicate"},
		{"bad id", func(s *BuildSpec) { s.Nodes[0].ID = "a b" }, "id"},
		{"missing group", func(s *BuildSpec) { s.Nodes[0].Group = "nope" }, "group"},
		{"dangling edge", func(s *BuildSpec) { s.Edges[0].To = "ghost" }, "does not exist"},
		{"self edge", func(s *BuildSpec) { s.Edges[0].To = s.Edges[0].From }, "itself"},
		{"bad direction", func(s *BuildSpec) { s.Direction = "diag" }, "direction"},
		{"huge size", func(s *BuildSpec) { s.Nodes[0].Width = 1e9 }, "size"},
		{"group cycle", func(s *BuildSpec) { s.Groups[0].Parent = "ns"; s.Groups[1].Parent = "edge" }, "ancestor"},
	}
	for _, c := range bad {
		spec := archSpec()
		c.mut(spec)
		d, page, layer := blankPage(t)
		_, _, err := BuildOps(spec, page, layer, map[string]bool{}, "t")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.want, err)
		}
		_ = d
	}
	// existing ids are refused, never overwritten
	d, page, layer := blankPage(t)
	if _, _, err := BuildOps(archSpec(), page, layer, map[string]bool{"gw": true}, "t"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("existing id: %v", err)
	}
	// cap per call
	big := &BuildSpec{}
	for i := 0; i < MaxBuildCells+1; i++ {
		big.Nodes = append(big.Nodes, BuildNode{ID: fmt.Sprintf("n%d", i), Label: "x"})
	}
	if _, _, err := BuildOps(big, page, layer, map[string]bool{}, "t"); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("cap: %v", err)
	}
	_ = d
}

func TestBuildEscapesLabels(t *testing.T) {
	d, page, layer := blankPage(t)
	spec := &BuildSpec{Nodes: []BuildNode{{ID: "a", Label: `<img src=x onerror=alert(1)> & "q"`}}}
	ops, _, err := BuildOps(spec, page, layer, map[string]bool{}, "t")
	if err != nil {
		t.Fatal(err)
	}
	if v := ops[0].Props["v"]; strings.Contains(v, "<img") || !strings.Contains(v, "&lt;img") {
		t.Fatalf("label not escaped: %q", v)
	}
	d.FillOrders(ops)
	if _, oe := d.ApplyBatch(ops); oe != nil {
		t.Fatalf("%+v", oe)
	}
}

// hierarchical layout repairs a diagram that was placed badly, and costs nothing on one that is already tidy.
func TestHierarchicalLayoutRepairsAndSettles(t *testing.T) {
	d, page, layer := blankPage(t)
	ops, _, err := BuildOps(archSpec(), page, layer, map[string]bool{}, "t")
	if err != nil {
		t.Fatal(err)
	}
	// wreck it: every top-level shape and every container piled at the origin
	for i := range ops {
		if ops[i].Kind == "v" {
			ops[i].Props["gx"], ops[i].Props["gy"] = "10", "10"
		}
	}
	d.FillOrders(ops)
	if _, oe := d.ApplyBatch(ops); oe != nil {
		t.Fatalf("%+v", oe)
	}
	before, _ := d.Lint(page)
	if before.Clean() {
		t.Fatal("wrecked diagram should not lint clean")
	}
	fix, err := d.LayoutOps(page, "hierarchical", "lx")
	if err != nil || len(fix) == 0 {
		t.Fatalf("layout: %v, %d ops", err, len(fix))
	}
	if _, oe := d.ApplyBatch(fix); oe != nil {
		t.Fatalf("layout ops rejected: %+v", oe)
	}
	after, _ := d.Lint(page)
	for _, i := range after.Issues {
		if i.Code == "overlap" || i.Code == "outside_container" || i.Code == "label_overflow" {
			t.Fatalf("still broken after layout:%s", dump(after))
		}
	}
	again, _ := d.LayoutOps(page, "hierarchical", "ly")
	if len(again) != 0 {
		t.Fatalf("layout is not stable: %d ops on a second run", len(again))
	}
}

func TestLintFindsProblems(t *testing.T) {
	d, page, layer := blankPage(t)
	mk := func(id, label string, x, y, w, h string) Op {
		return Op{ID: "o" + id, T: OpAddCell, Page: page, Cell: id, Kind: "v", Parent: layer,
			Props: map[string]string{"v": label, "gx": x, "gy": y, "gw": w, "gh": h, "st:whiteSpace": "wrap", "st:html": "1"}}
	}
	ops := []Op{
		mk("a", "A", "0", "0", "120", "60"),
		mk("b", "B", "60", "20", "120", "60"), // overlaps a
		mk("c", "a very long label that cannot possibly fit into this tiny shape at all", "400", "0", "60", "30"),
		mk("l", "Left", "0", "300", "120", "60"),
		mk("m", "Middle", "300", "300", "120", "60"),
		mk("r", "Right", "600", "300", "120", "60"),
		{ID: "oe", T: OpAddCell, Page: page, Cell: "e1", Kind: "e", Parent: layer, Props: map[string]string{"src": "l", "tgt": "r", "gx": "0", "gy": "0", "gw": "0", "gh": "0", "grel": "1"}},
	}
	d.FillOrders(ops)
	if _, oe := d.ApplyBatch(ops); oe != nil {
		t.Fatalf("%+v", oe)
	}
	lr, _ := d.Lint(page)
	have := map[string]bool{}
	for _, i := range lr.Issues {
		have[i.Code] = true
	}
	for _, code := range []string{"overlap", "label_overflow", "edge_through_shape"} {
		if !have[code] {
			t.Errorf("missing %s in:%s", code, dump(lr))
		}
	}
}

// Quadratic work is bounded: a huge page is linted partially (and says so) and refuses hierarchical layout.
func TestLargeDiagramsAreBounded(t *testing.T) {
	d, page, layer := blankPage(t)
	var ops []Op
	for i := 0; i < lintMaxShapes+10; i++ {
		ops = append(ops, Op{ID: fmt.Sprintf("o%d", i), T: OpAddCell, Page: page, Cell: fmt.Sprintf("n%d", i), Kind: "v", Parent: layer,
			Props: map[string]string{"v": "x", "gx": fmt.Sprint(i * 10), "gy": "0", "gw": "100", "gh": "40"}}) // all overlapping
	}
	d.FillOrders(ops)
	if _, oe := d.ApplyBatch(ops); oe != nil {
		t.Fatalf("%+v", oe)
	}
	lr, err := d.Lint(page)
	if err != nil {
		t.Fatal(err)
	}
	partial, overlap := false, false
	for _, i := range lr.Issues {
		partial = partial || i.Code == "lint_partial"
		overlap = overlap || i.Code == "overlap"
	}
	if !partial || overlap {
		t.Fatalf("large page must be linted partially without the quadratic checks: partial=%v overlap=%v", partial, overlap)
	}
	more := make([]Op, 0, MaxLayoutCells)
	for i := 0; i < MaxLayoutCells; i++ {
		more = append(more, Op{ID: fmt.Sprintf("m%d", i), T: OpAddCell, Page: page, Cell: fmt.Sprintf("m%d", i), Kind: "v", Parent: layer,
			Props: map[string]string{"v": "x", "gx": "0", "gy": "0", "gw": "10", "gh": "10"}})
	}
	d.FillOrders(more)
	if _, oe := d.ApplyBatch(more); oe != nil {
		t.Fatalf("%+v", oe)
	}
	if _, err := d.LayoutOps(page, "hierarchical", "x"); err == nil || !strings.Contains(err.Error(), "up to") {
		t.Fatalf("hierarchical layout of a huge page must be refused: %v", err)
	}
}
