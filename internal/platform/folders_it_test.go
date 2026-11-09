package platform

// Integration test (needs the lab PostgreSQL/S3/Redis, see platform_it_test.go; skipped unless DRAWIO_IT=1):
// personal folders and per-user placement (ADR-0020).

import (
	"fmt"
	"testing"
	"time"
)

func docFolder(out map[string]any, id string) (found bool, folder any, placed bool) {
	items, _ := out["documents"].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m["id"] == id {
			return true, m["folderId"], m["placed"] == true
		}
	}
	return false, nil, false
}

func TestIT_Folders(t *testing.T) {
	e := newIT(t)
	alice := who{user: "alice", roles: []string{"user"}}
	ai := who{user: "ai-1", roles: []string{"user"}, typ: "ai", obo: "alice"}
	tag := fmt.Sprint(time.Now().UnixNano() % 1e9)
	bob := who{user: "bob" + tag, roles: []string{"user"}} // fresh every run: the test files a share into bob's own folder and leaves it

	mk := func(w who, name string, parent any) string {
		c, o := e.call(w, "POST", "/api/v1/folders", map[string]any{"name": name, "parentId": parent})
		if c != 201 {
			t.Fatalf("create folder %q: %d %v", name, c, o)
		}
		return o["folder"].(map[string]any)["id"].(string)
	}
	a := mk(alice, "A"+tag, nil)
	b := mk(alice, "B", a)
	if c, _ := e.call(alice, "POST", "/api/v1/folders", map[string]any{"name": "b", "parentId": a}); c != 409 {
		t.Fatalf("duplicate name (case-insensitive) must be 409: %d", c)
	}
	// no cycles: A cannot go into its own child
	if c, _ := e.call(alice, "PATCH", "/api/v1/folders/"+a, map[string]any{"parentId": b}); c != 400 {
		t.Fatalf("cycle must be 400: %d", c)
	}
	// folders are private: bob can neither see, nor use, nor touch alice's folder
	if c, o := e.call(bob, "GET", "/api/v1/folders", nil); c != 200 || len(o["folders"].([]any)) != 0 {
		t.Fatalf("bob must see no folders: %d %v", c, o)
	}
	if c, _ := e.call(bob, "PATCH", "/api/v1/folders/"+a, map[string]any{"name": "x"}); c != 404 {
		t.Fatalf("foreign folder rename must be 404: %d", c)
	}
	if c, _ := e.call(ai, "GET", "/api/v1/folders", nil); c != 403 {
		t.Fatalf("AI actors must be refused: %d", c)
	}
	// depth limit (8 levels)
	cur := b
	for i := 3; i <= 8; i++ {
		cur = mk(alice, fmt.Sprintf("L%d", i), cur)
	}
	if c, _ := e.call(alice, "POST", "/api/v1/folders", map[string]any{"name": "L9", "parentId": cur}); c != 422 {
		t.Fatalf("depth 9 must be refused: %d", c)
	}

	// own diagram: default root, move into B, back to root
	_, out := e.call(alice, "POST", "/api/v1/documents", map[string]any{"title": "Fold " + tag})
	id := out["document"].(map[string]any)["id"].(string)
	if c, _ := e.call(alice, "PUT", "/api/v1/documents/"+id+"/placement", map[string]any{"folderId": b}); c != 200 {
		t.Fatalf("place: %d", c)
	}
	_, l := e.call(alice, "GET", "/api/v1/documents", nil)
	if f, fid, pl := docFolder(l, id); !f || fid != b || !pl {
		t.Fatalf("alice must see it in B: %v %v %v", f, fid, pl)
	}
	// a share lands in "Shared with me" (placed=false) and bob may file it in his own folder; alice's view is unaffected
	e.shareWith(alice, id, bob, "viewer")
	_, bl := e.call(bob, "GET", "/api/v1/documents", nil)
	if f, fid, pl := docFolder(bl, id); !f || fid != nil || pl {
		t.Fatalf("bob: default spot expected: %v %v %v", f, fid, pl)
	}
	bf := mk(bob, "Dari Alice", nil)
	if c, _ := e.call(bob, "PUT", "/api/v1/documents/"+id+"/placement", map[string]any{"folderId": bf}); c != 200 {
		t.Fatalf("bob place: %d", c)
	}
	if c, _ := e.call(bob, "PUT", "/api/v1/documents/"+id+"/placement", map[string]any{"folderId": a}); c != 404 {
		t.Fatalf("filing into someone else's folder must be 404: %d", c)
	}
	_, bl = e.call(bob, "GET", "/api/v1/documents", nil)
	if _, fid, pl := docFolder(bl, id); fid != bf || !pl {
		t.Fatalf("bob must see it in his folder: %v %v", fid, pl)
	}
	_, l = e.call(alice, "GET", "/api/v1/documents", nil)
	if _, fid, _ := docFolder(l, id); fid != b {
		t.Fatalf("alice's view must not change: %v", fid)
	}
	// back to "Shared with me"
	if c, _ := e.call(bob, "PUT", "/api/v1/documents/"+id+"/placement", map[string]any{"inbox": true}); c != 200 {
		t.Fatalf("inbox: %d", c)
	}
	_, bl = e.call(bob, "GET", "/api/v1/documents", nil)
	if _, _, pl := docFolder(bl, id); pl {
		t.Fatal("inbox must clear the placement")
	}
	// a stranger cannot place a diagram they cannot read
	carol := who{user: "carol", roles: []string{"user"}}
	if c, _ := e.call(carol, "PUT", "/api/v1/documents/"+id+"/placement", map[string]any{"folderId": nil}); c != 404 {
		t.Fatalf("stranger placement must be 404: %d", c)
	}

	// an identically named child must be able to move up when its parent is deleted (Archive/Archive)
	ar := mk(alice, "Archive"+tag, nil)
	mk(alice, "Archive"+tag, ar)
	if c, _ := e.call(alice, "DELETE", "/api/v1/folders/"+ar, nil); c != 204 {
		t.Fatalf("deleting Archive/Archive: %d", c)
	}

	// deleting B moves the diagram and nothing else up to A
	if c, _ := e.call(alice, "DELETE", "/api/v1/folders/"+b, nil); c != 204 {
		t.Fatalf("delete folder: %d", c)
	}
	_, l = e.call(alice, "GET", "/api/v1/documents", nil)
	if _, fid, _ := docFolder(l, id); fid != a {
		t.Fatalf("contents must move up to A: %v", fid)
	}
}
