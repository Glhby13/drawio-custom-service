package mcpsrv

import (
	"encoding/json"
	"strings"
	"testing"
)

// The tool catalogue is what the model reads: it must be strictly valid JSON Schema (strict clients drop the whole list on one bad
// tool) and must steer the model to the server-side layout instead of hand-placed coordinates.
func TestCatalogueIsValidAndGuidesTowardsBuildAndLint(t *testing.T) {
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if r, has := x["required"]; has && r == nil {
				t.Errorf("%s: required is null", path)
			}
			if x["type"] == "array" {
				if _, ok := x["items"].(map[string]any); !ok {
					t.Errorf("%s: array without items schema", path)
				}
			}
			for k, c := range x {
				walk(path+"."+k, c)
			}
		}
	}
	names := map[string]bool{}
	for _, d := range toolDefs() {
		name, _ := d["name"].(string)
		names[name] = true
		if desc, _ := d["description"].(string); len(desc) < 20 {
			t.Errorf("%s: description too thin for a model to choose by", name)
		}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var back any
		_ = json.Unmarshal(b, &back)
		walk(name, back)
	}
	for _, want := range []string{"build_diagram", "lint_diagram", "layout"} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}
	b, _ := json.Marshal(toolDefs())
	for _, want := range []string{`"hierarchical"`, `"hierarchical-lr"`, `"database"`, `"queue"`, `"teal"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("schema lacks %s", want)
		}
	}
	for _, in := range []string{instructionsFor(actor{}), instructionsFor(actor{DocScope: "d1"})} {
		for _, want := range []string{"build_diagram", "lint_diagram", "layout", "Do NOT choose x/y"} {
			if !strings.Contains(in, want) {
				t.Errorf("instructions lack %q", want)
			}
		}
	}
	// a diagram token cannot create diagrams and need not name the document, also for the new tools
	for _, d := range toolDefsFor(actor{DocScope: "d1"}) {
		if d["name"] == "create_diagram" {
			t.Error("create_diagram offered to a per-diagram token")
		}
		if d["name"] == "build_diagram" {
			schema, _ := d["inputSchema"].(map[string]any)
			for _, r := range schema["required"].([]string) {
				if r == "document" {
					t.Error("build_diagram still requires document for a per-diagram token")
				}
			}
		}
	}
}

func TestConnectNeverCreatesDanglingEdges(t *testing.T) {
	srv, seen, _ := setup(t)
	before := len(*seen)
	_, out, _ := rpc(t, srv.URL, "good", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"connect","arguments":{"document":"d1","source":"ghost","target":"1"}}}`)
	if !strings.Contains(out, `"isError":true`) || !strings.Contains(out, "ghost") || !strings.Contains(out, "get_diagram") {
		t.Fatalf("a missing endpoint must be a clear tool error: %s", out)
	}
	for _, s := range (*seen)[before:] {
		if strings.Contains(s, "/operations") {
			t.Fatalf("an operation was sent for an invalid connect: %s", s)
		}
	}
	_, out, _ = rpc(t, srv.URL, "good", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"connect","arguments":{"document":"d1","source":"1","target":"0","label":"a <b> & c"}}}`)
	if !strings.Contains(out, "Added edge") {
		t.Fatalf("connect: %s", out)
	}
	last := (*seen)[len(*seen)-1]
	for _, want := range []string{`"st:edgeStyle":"orthogonalEdgeStyle"`, `"src":"1"`} {
		if !strings.Contains(last, want) {
			t.Errorf("edge operation lacks %s: %s", want, last)
		}
	}
	if v := opProps(t, last)["v"]; v != "a &lt;b&gt; &amp; c" {
		t.Errorf("edge label not escaped: %q", v)
	}
}

func TestAddShapeDefaultsAreReadableAndSafe(t *testing.T) {
	srv, seen, _ := setup(t)
	_, out, _ := rpc(t, srv.URL, "good", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_shape","arguments":{"document":"d1","label":"<img src=x onerror=alert(1)>","x":1e12,"width":-5}}}`)
	if !strings.Contains(out, "Added shape") {
		t.Fatalf("add_shape: %s", out)
	}
	last := (*seen)[len(*seen)-1]
	for _, want := range []string{`"st:html":"1"`, `"st:whiteSpace":"wrap"`, `"gx":"100000"`, `"gw":"10"`} {
		if !strings.Contains(last, want) {
			t.Errorf("shape operation lacks %s: %s", want, last)
		}
	}
	if v := opProps(t, last)["v"]; v != "&lt;img src=x onerror=alert(1)&gt;" {
		t.Errorf("label markup was not escaped: %q", v)
	}
}

// opProps extracts the props of the first operation from a recorded "METHOD path body" request.
func opProps(t *testing.T, rec string) map[string]string {
	t.Helper()
	i := strings.Index(rec, "{")
	var b struct {
		Ops []struct {
			Props map[string]string `json:"props"`
		} `json:"ops"`
	}
	if i < 0 || json.Unmarshal([]byte(rec[i:]), &b) != nil || len(b.Ops) == 0 {
		t.Fatalf("not an operations request: %s", rec)
	}
	return b.Ops[0].Props
}
