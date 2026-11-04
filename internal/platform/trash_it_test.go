package platform

// Integration test (needs the lab PostgreSQL/S3/Redis, see platform_it_test.go; skipped unless DRAWIO_IT=1):
// soft delete, the trash list and restoring a diagram.

import (
	"fmt"
	"testing"
	"time"
)

func inList(out map[string]any, id string) bool {
	items, _ := out["documents"].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m["id"] == id {
			return true
		}
	}
	return false
}

func TestIT_TrashAndRestore(t *testing.T) {
	e := newIT(t)
	alice := who{user: "alice", roles: []string{"user"}}
	bob := who{user: "bob", roles: []string{"user"}}
	carol := who{user: "carol", roles: []string{"user"}}
	admin := who{user: "boss", roles: []string{"user", "admin"}}
	ai := who{user: "ai-1", roles: []string{"user"}, typ: "ai", obo: "alice"}
	tag := fmt.Sprint(time.Now().UnixNano() % 1e9)

	code, out := e.call(alice, "POST", "/api/v1/documents", map[string]any{"title": "Trash " + tag})
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["document"].(map[string]any)["id"].(string)
	if c, o := e.call(alice, "PUT", "/api/v1/documents/"+id+"/permissions", map[string]any{"subject": "bob", "role": "editor"}); c != 200 {
		t.Fatalf("share: %d %v", c, o)
	}

	// only the owner may delete: an editor and an application admin are refused, a stranger sees nothing
	if c, _ := e.call(bob, "DELETE", "/api/v1/documents/"+id, nil); c != 403 {
		t.Fatalf("an editor must not delete: %d", c)
	}
	if c, _ := e.call(admin, "DELETE", "/api/v1/documents/"+id, nil); c != 403 {
		t.Fatalf("an application admin must not delete somebody else's diagram: %d", c)
	}
	if c, _ := e.call(carol, "DELETE", "/api/v1/documents/"+id, nil); c != 404 {
		t.Fatalf("a stranger must get 404: %d", c)
	}
	if c, _ := e.call(alice, "DELETE", "/api/v1/documents/"+id, nil); c != 200 {
		t.Fatalf("owner delete: %d", c)
	}
	if c, _ := e.call(alice, "GET", "/api/v1/documents/"+id, nil); c != 404 {
		t.Fatalf("a deleted diagram must be gone: %d", c)
	}
	if _, l := e.call(alice, "GET", "/api/v1/documents", nil); inList(l, id) {
		t.Fatal("a deleted diagram must leave the normal list")
	}

	// the trash: only the owner sees it (not an admin, editor or stranger); AI actors are refused
	if _, tr := e.call(alice, "GET", "/api/v1/trash", nil); !inList(tr, id) {
		t.Fatalf("owner trash must list it: %v", tr)
	}
	if _, tr := e.call(admin, "GET", "/api/v1/trash", nil); inList(tr, id) {
		t.Fatal("an admin's trash must not list somebody else's diagram")
	}
	// the sharee's list no longer shows it at all (it is not "trashed" for them, it is gone)
	if _, l := e.call(bob, "GET", "/api/v1/documents", nil); inList(l, id) {
		t.Fatal("a shared diagram in the owner's trash must vanish from the sharee's list")
	}
	if _, tr := e.call(bob, "GET", "/api/v1/trash", nil); inList(tr, id) {
		t.Fatal("an editor's trash must not list somebody else's diagram")
	}
	if c, _ := e.call(ai, "GET", "/api/v1/trash", nil); c != 403 {
		t.Fatalf("AI trash: %d", c)
	}

	// restore: not for the editor, not for a stranger (both 404, no existence oracle), not for an AI actor
	for _, w := range []who{bob, carol, admin} {
		if c, _ := e.call(w, "POST", "/api/v1/documents/"+id+"/undelete", nil); c != 404 {
			t.Fatalf("%s must not restore: %d", w.user, c)
		}
	}
	if c, _ := e.call(ai, "POST", "/api/v1/documents/"+id+"/undelete", nil); c != 403 {
		t.Fatalf("AI restore: %d", c)
	}
	if c, o := e.call(alice, "POST", "/api/v1/documents/"+id+"/undelete", nil); c != 200 || o["restored"] != true {
		t.Fatalf("owner restore: %d %v", c, o)
	}
	if c, _ := e.call(alice, "GET", "/api/v1/documents/"+id, nil); c != 200 {
		t.Fatalf("a restored diagram must be back: %d", c)
	}
	if c, _ := e.call(bob, "GET", "/api/v1/documents/"+id, nil); c != 200 {
		t.Fatalf("shares survive delete and restore: %d", c)
	}
	if _, tr := e.call(alice, "GET", "/api/v1/trash", nil); inList(tr, id) {
		t.Fatal("a restored diagram must leave the trash")
	}
	if c, _ := e.call(alice, "POST", "/api/v1/documents/"+id+"/undelete", nil); c != 404 {
		t.Fatalf("restoring a diagram that is not in the trash: %d", c)
	}

	// the list tells the owner who a diagram is shared with; the sharee gets no such list
	_, l := e.call(alice, "GET", "/api/v1/documents", nil)
	if sw := sharedWithOf(l, id); len(sw) != 1 || sw[0].(map[string]any)["subject"] != "bob" || sw[0].(map[string]any)["role"] != "editor" {
		t.Fatalf("owner list must show the share: %v", sw)
	}
	_, l = e.call(bob, "GET", "/api/v1/documents", nil)
	if sw := sharedWithOf(l, id); len(sw) != 0 || !inList(l, id) {
		t.Fatalf("a sharee must see the diagram but not who else has it: %v", sw)
	}
}

func sharedWithOf(out map[string]any, id string) []any {
	items, _ := out["documents"].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m["id"] == id {
			sw, _ := m["sharedWith"].([]any)
			return sw
		}
	}
	return nil
}
