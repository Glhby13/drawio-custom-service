package platform

import (
	"math"
	"strings"
	"testing"
)

func f(v float64) *float64 { return &v }

func TestSanitizeActRoles(t *testing.T) {
	cells := []string{"a1"}
	if o, ok := sanitizeAct(RoleViewer, &ActIn{State: ActEditing, Cells: cells}); !ok || o.State != ActSelecting {
		t.Fatalf("a viewer cannot show editing, it is shown as a selection: %+v", o)
	}
	if o, ok := sanitizeAct(RoleCommenter, &ActIn{State: ActDragging, Cells: cells, Preview: &ActPreview{Dx: f(5), Dy: f(5)}}); !ok || o.State != ActSelecting || o.Preview != nil {
		t.Fatalf("a commenter cannot show dragging, and the preview is dropped with it: %+v", o)
	}
	for _, role := range []string{RoleEditor, RoleOwner} {
		if o, ok := sanitizeAct(role, &ActIn{State: ActEditing, Cells: cells}); !ok || o.State != ActEditing {
			t.Fatalf("%s may edit: %+v", role, o)
		}
	}
	if _, ok := sanitizeAct(RoleEditor, &ActIn{State: "applying", Cells: cells}); ok {
		t.Fatal("browsers cannot claim the AI-only state")
	}
	if _, ok := sanitizeAct(RoleEditor, &ActIn{State: "bogus"}); ok {
		t.Fatal("unknown state accepted")
	}
	if _, ok := sanitizeAct(RoleEditor, nil); ok {
		t.Fatal("nil accepted")
	}
}

func TestSanitizeActContent(t *testing.T) {
	long := make([]string, 200)
	for i := range long {
		long[i] = "c" + strings.Repeat("x", i%3)
	}
	o, _ := sanitizeAct(RoleEditor, &ActIn{State: ActSelecting, Cells: append(long, "bad id", "<script>", strings.Repeat("a", 200))})
	if len(o.Cells) != maxActCells {
		t.Fatalf("cells capped: %d", len(o.Cells))
	}
	for _, c := range o.Cells {
		if !actIDRe.MatchString(c) {
			t.Fatalf("unsafe id %q", c)
		}
	}
	if o, _ := sanitizeAct(RoleEditor, &ActIn{State: ActSelecting, Cells: []string{"bad id"}}); o.State != ActIdle {
		t.Fatalf("no valid cell -> idle: %+v", o)
	}
	d := strings.Repeat("é", 2000) + string([]byte{0xff})
	o, _ = sanitizeAct(RoleEditor, &ActIn{State: ActEditing, Cells: []string{"a"}, Draft: &d})
	if o.Draft == nil || len([]rune(*o.Draft)) != maxDraftRune {
		t.Fatalf("draft capped at %d runes", maxDraftRune)
	}
	d2 := "secret"
	if o, _ := sanitizeAct(RoleEditor, &ActIn{State: ActDragging, Cells: []string{"a"}, Draft: &d2, Preview: &ActPreview{Dx: f(math.Inf(1)), Dy: f(3)}}); o.Draft != nil || (o.Preview != nil && o.Preview.Dx != nil) {
		t.Fatalf("draft only while editing, non-finite numbers dropped: %+v", o)
	}
	if o, _ := sanitizeAct(RoleEditor, &ActIn{State: ActResizing, Cells: []string{"a"}, Preview: &ActPreview{X: f(1e12), W: f(10), H: f(20)}}); o.Preview == nil || o.Preview.X != nil || o.Preview.W == nil {
		t.Fatalf("out-of-range coordinate dropped, the rest kept: %+v", o.Preview)
	}
	if o, _ := sanitizeAct(RoleViewer, &ActIn{State: ActIdle, Cells: []string{"a"}}); o.State != ActIdle || len(o.Cells) != 0 {
		t.Fatal("idle carries nothing")
	}
}

func TestDocHandleActsExpireAndGoneRemoves(t *testing.T) {
	dh := &docHandle{sessions: map[*Session]struct{}{}}
	dh.setAct(&ActMsg{SID: "s1", State: ActEditing, Cells: []string{"a"}, Exp: 1}) // already expired
	dh.setAct(&ActMsg{SID: "s2", State: ActSelecting, Cells: []string{"a"}, Exp: 1 << 60})
	if snap := dh.actSnapshot("none"); len(snap) != 1 || snap[0].SID != "s2" {
		t.Fatalf("snapshot must drop expired entries: %+v", snap)
	}
	if snap := dh.actSnapshot("s2"); len(snap) != 0 {
		t.Fatal("a session never receives its own activity")
	}
	dh.setAct(&ActMsg{SID: "s2", State: ActGone})
	if len(dh.acts) != 0 {
		t.Fatal("gone removes the entry")
	}
}
