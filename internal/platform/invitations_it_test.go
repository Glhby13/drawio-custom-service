package platform

// Integration test of invitation-based sharing (ADR-0021). Skipped unless DRAWIO_IT=1 (see platform_it_test.go).

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
)

func errCode(o map[string]any) string {
	if m, ok := o["error"].(map[string]any); ok {
		s, _ := m["code"].(string)
		return s
	}
	return ""
}

func TestIT_Invitations(t *testing.T) {
	e := newIT(t)
	ctx := context.Background()
	sfx := strings.ToLower(newID(4))
	mk := func(n string) who { return who{user: "inv-" + n + "-" + sfx, roles: []string{"user"}} }
	alice, bob, carol, dave, erin := mk("alice"), mk("bob"), mk("carol"), mk("dave"), mk("erin")
	title := "invitations it " + sfx
	base := func(doc string) string { return "/api/v1/documents/" + doc + "/invitations" }

	newDoc := func(owner who, title string) string {
		t.Helper()
		c, o := e.call(owner, "POST", "/api/v1/documents", map[string]any{"title": title})
		if c != 201 {
			t.Fatalf("create doc: %d %v", c, o)
		}
		return o["document"].(map[string]any)["id"].(string)
	}
	// invite creates an invitation as owner and returns (status, body); on 201 also the token and the invitation id.
	invite := func(owner who, doc string, body map[string]any) (int, map[string]any, string, string) {
		t.Helper()
		c, o := e.call(owner, "POST", base(doc), body)
		tok, _ := o["token"].(string)
		id := ""
		if inv, ok := o["invitation"].(map[string]any); ok {
			id, _ = inv["id"].(string)
		}
		return c, o, tok, id
	}
	mustInvite := func(owner who, doc string, body map[string]any) (string, string) {
		t.Helper()
		c, o, tok, id := invite(owner, doc, body)
		if c != 201 || tok == "" || id == "" {
			t.Fatalf("invite %v: %d %v", body, c, o)
		}
		return tok, id
	}
	accept := func(w who, tok string) (int, map[string]any) {
		return e.call(w, "POST", "/api/v1/invitations/"+tok+"/accept", nil)
	}
	roleOf := func(w who, doc string) (int, any) {
		c, o := e.call(w, "GET", "/api/v1/documents/"+doc, nil)
		return c, o["role"]
	}
	svc, _ := e.gw.Sign(identity.Claims{Subject: "gateway-auth", Audience: Audience, ActorType: identity.ActorService}, time.Minute)

	doc := newDoc(alice, title)

	// ---- a link is multi-use: two different users accept it, both get the role, the preview names the diagram and the inviter
	linkTok, linkID := mustInvite(alice, doc, map[string]any{"kind": "link", "role": "viewer"})
	if c, o := e.call(bob, "GET", "/api/v1/invitations/"+linkTok, nil); c != 200 ||
		o["document"].(map[string]any)["title"] != title || o["inviter"].(map[string]any)["username"] != alice.user || o["role"] != "viewer" {
		t.Fatalf("preview: %d %v", c, o)
	}
	for _, w := range []who{bob, carol} {
		if c, o := accept(w, linkTok); c != 200 || o["role"] != "viewer" || o["documentId"] != doc {
			t.Fatalf("accept link %s: %d %v", w.user, c, o)
		}
		if c, r := roleOf(w, doc); c != 200 || r != "viewer" {
			t.Fatalf("%s after accept: %d %v", w.user, c, r)
		}
	}
	var useCount int
	if err := e.srv.store.pool.QueryRow(ctx, `SELECT use_count FROM invitations WHERE id=$1`, linkID).Scan(&useCount); err != nil || useCount != 2 {
		t.Fatalf("use_count %d (%v), want 2", useCount, err)
	}
	// a link stays pending after use
	_, list := e.call(alice, "GET", base(doc), nil)
	found := false
	for _, x := range list["invitations"].([]any) {
		if m := x.(map[string]any); m["id"] == linkID {
			found = true
			if m["state"] != "pending" || m["useCount"] != float64(2) {
				t.Fatalf("link in list: %v", m)
			}
		}
	}
	if !found {
		t.Fatal("link missing from the owner's list")
	}
	// the list never carries a token or its hash; the table stores only a 64-hex hash
	raw, _ := json.Marshal(list)
	for _, bad := range []string{linkTok, "token_hash", "tokenHash", "\"token\""} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("the list leaks %q: %s", bad, raw)
		}
	}
	var stored string
	if err := e.srv.store.pool.QueryRow(ctx, `SELECT token_hash FROM invitations WHERE id=$1`, linkID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if _, err := hex.DecodeString(stored); err != nil || len(stored) != 64 || stored == linkTok || strings.Contains(stored, linkTok) || stored != hashToken(linkTok) {
		t.Fatalf("stored token_hash %q is not the 64-hex hash of the token", stored)
	}

	// ---- a user invitation is bound to its subject
	doc2 := newDoc(alice, title+" 2")
	uTok, _ := mustInvite(alice, doc2, map[string]any{"kind": "user", "subject": bob.user, "role": "editor", "sendEmail": false})
	c, o := e.call(carol, "GET", "/api/v1/invitations/"+uTok, nil)
	if c != 403 || errCode(o) != "invitation_not_for_you" {
		t.Fatalf("preview by another user: %d %v", c, o)
	}
	c, o = accept(carol, uTok)
	if c != 403 || errCode(o) != "invitation_not_for_you" {
		t.Fatalf("accept by another user: %d %v", c, o)
	}
	if b, _ := json.Marshal(o); strings.Contains(string(b), title) || strings.Contains(string(b), doc2) || strings.Contains(string(b), alice.user) {
		t.Fatalf("refusal teaches the stranger something: %s", b)
	}
	if c, _ := roleOf(carol, doc2); c != 404 {
		t.Fatalf("carol must still see nothing: %d", c)
	}
	if c, o := accept(bob, uTok); c != 200 || o["role"] != "editor" {
		t.Fatalf("accept by the subject: %d %v", c, o)
	}
	if c, r := roleOf(bob, doc2); c != 200 || r != "editor" {
		t.Fatalf("bob after accept: %d %v", c, r)
	}
	if c, o := accept(bob, uTok); c != 404 || errCode(o) != "invitation_invalid" {
		t.Fatalf("second accept: %d %v", c, o)
	}
	// somebody who already has access cannot be invited to the same or less
	if c, o, _, _ := invite(alice, doc2, map[string]any{"kind": "user", "subject": bob.user, "role": "viewer"}); c != 409 || errCode(o) != "already_has_access" {
		t.Fatalf("invite someone who already has access: %d %v", c, o)
	}

	// ---- decline leaves no access, and the invitation is gone
	doc3 := newDoc(alice, title+" 3")
	dTok, _ := mustInvite(alice, doc3, map[string]any{"kind": "user", "subject": dave.user, "role": "viewer", "sendEmail": false})
	if c, o := e.call(dave, "POST", "/api/v1/invitations/"+dTok+"/decline", nil); c != 200 || o["declined"] != true {
		t.Fatalf("decline: %d %v", c, o)
	}
	if c, _ := roleOf(dave, doc3); c != 404 {
		t.Fatalf("declined invitation gave access: %d", c)
	}
	if c, o := accept(dave, dTok); c != 404 || errCode(o) != "invitation_invalid" {
		t.Fatalf("accept after decline: %d %v", c, o)
	}

	// ---- expired and revoked invitations answer like an unknown token
	xTok, xID := mustInvite(alice, doc3, map[string]any{"kind": "link", "role": "viewer", "expiresInDays": 1})
	if _, err := e.srv.store.pool.Exec(ctx, `UPDATE invitations SET expires_at=now()-interval '1 minute' WHERE id=$1`, xID); err != nil {
		t.Fatal(err)
	}
	if c, o := accept(erin, xTok); c != 404 || errCode(o) != "invitation_invalid" {
		t.Fatalf("expired: %d %v", c, o)
	}
	if c, o := e.call(erin, "GET", "/api/v1/invitations/"+xTok, nil); c != 404 {
		t.Fatalf("expired preview: %d %v", c, o)
	}
	rTok, rID := mustInvite(alice, doc3, map[string]any{"kind": "link", "role": "viewer"})
	if c, o := e.call(alice, "DELETE", base(doc3)+"/"+rID, nil); c != 200 || o["state"] != "revoked" {
		t.Fatalf("revoke: %d %v", c, o)
	}
	if c, o := accept(erin, rTok); c != 404 || errCode(o) != "invitation_invalid" {
		t.Fatalf("revoked: %d %v", c, o)
	}
	if c, _ := roleOf(erin, doc3); c != 404 {
		t.Fatalf("erin got access from a dead invitation: %d", c)
	}
	if c, o := accept(erin, "dsi_"+strings.Repeat("A", 43)); c != 404 || errCode(o) != "invitation_invalid" {
		t.Fatalf("unknown token: %d %v", c, o)
	}
	if c, o := accept(erin, "garbage"); c != 404 || errCode(o) != "invitation_invalid" {
		t.Fatalf("malformed token: %d %v", c, o)
	}

	// ---- the owner cannot accept their own invitation
	oTok, _ := mustInvite(alice, doc3, map[string]any{"kind": "link", "role": "editor"})
	if c, o := accept(alice, oTok); c != 409 || errCode(o) != "invitation_owner" {
		t.Fatalf("owner accepts own: %d %v", c, o)
	}

	// ---- accepting never lowers an existing role
	doc4 := newDoc(alice, title+" 4")
	e.shareWith(alice, doc4, bob, "editor")
	vTok, _ := mustInvite(alice, doc4, map[string]any{"kind": "link", "role": "viewer"})
	if c, o := accept(bob, vTok); c != 200 || o["role"] != "editor" {
		t.Fatalf("viewer link for an editor: %d %v", c, o)
	}
	if c, r := roleOf(bob, doc4); c != 200 || r != "editor" {
		t.Fatalf("role lowered: %d %v", c, r)
	}

	// ---- expiry: default 7 days, 0 = never, 1..365 ok, otherwise 400
	expOf := func(id string) *time.Time {
		var x *time.Time
		if err := e.srv.store.pool.QueryRow(ctx, `SELECT expires_at FROM invitations WHERE id=$1`, id).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	_, defID := mustInvite(alice, doc4, map[string]any{"kind": "link", "role": "viewer"})
	if x := expOf(defID); x == nil || x.Before(time.Now().Add(7*24*time.Hour-time.Minute)) || x.After(time.Now().Add(7*24*time.Hour+time.Minute)) {
		t.Fatalf("default expiry %v, want ~7 days", x)
	}
	_, neverID := mustInvite(alice, doc4, map[string]any{"kind": "link", "role": "viewer", "expiresInDays": 0})
	if x := expOf(neverID); x != nil {
		t.Fatalf("expiresInDays 0 must store NULL, got %v", x)
	}
	for _, d := range []int{366, -1} {
		if c, o, _, _ := invite(alice, doc4, map[string]any{"kind": "link", "role": "viewer", "expiresInDays": d}); c != 400 {
			t.Fatalf("expiresInDays %d: %d %v", d, c, o)
		}
	}
	if c, o, _, _ := invite(alice, doc4, map[string]any{"kind": "link", "role": "commenter"}); c != 400 {
		t.Fatalf("role commenter: %d %v", c, o)
	}
	if c, o, _, _ := invite(alice, doc4, map[string]any{"kind": "link", "role": "owner"}); c != 400 {
		t.Fatalf("role owner: %d %v", c, o)
	}
	if c, o, _, _ := invite(alice, doc4, map[string]any{"kind": "other", "role": "viewer"}); c != 400 {
		t.Fatalf("kind other: %d %v", c, o)
	}

	// ---- only the owner manages invitations (viewer, editor, stranger refused)
	eTok, eID := mustInvite(alice, doc4, map[string]any{"kind": "link", "role": "viewer"})
	_ = eTok
	e.shareWith(alice, doc4, carol, "viewer")
	for _, w := range []who{bob /* editor */, carol /* viewer */, dave /* stranger */} {
		for _, req := range []struct {
			method, path string
			body         any
		}{
			{"POST", base(doc4), map[string]any{"kind": "link", "role": "viewer"}},
			{"GET", base(doc4), nil},
			{"DELETE", base(doc4) + "/" + eID, nil},
			{"POST", base(doc4) + "/" + eID + "/resend", nil},
		} {
			if c, _ := e.call(w, req.method, req.path, req.body); c != 403 && c != 404 {
				t.Fatalf("%s %s %s: %d, want 403/404", w.user, req.method, req.path, c)
			}
		}
	}

	// ---- AI actors cannot create or accept invitations
	ai := who{user: "ai-1", roles: []string{"user"}, typ: identity.ActorAI, obo: alice.user}
	if c, o, _, _ := invite(ai, doc4, map[string]any{"kind": "link", "role": "viewer"}); c != 403 && c != 404 {
		t.Fatalf("AI creates an invitation: %d %v", c, o)
	}
	acTok, _ := mustInvite(alice, doc4, map[string]any{"kind": "link", "role": "viewer"})
	if c, o := accept(ai, acTok); c != 403 {
		t.Fatalf("AI accepts an invitation: %d %v", c, o)
	}

	// ---- direct sharing with a new human or with everybody is gone; changing/removing an existing role still works
	perm := "/api/v1/documents/" + doc4 + "/permissions"
	if c, o := e.call(alice, "PUT", perm, map[string]any{"subject": erin.user, "role": "viewer"}); c != 400 || errCode(o) != "use_invitation" {
		t.Fatalf("putPerm for a new human: %d %v", c, o)
	}
	if c, o := e.call(alice, "PUT", perm, map[string]any{"subject": "*", "role": "viewer"}); c != 400 {
		t.Fatalf("putPerm '*': %d %v", c, o)
	}
	if c, _ := roleOf(erin, doc4); c != 404 {
		t.Fatalf("a refused grant leaked access: %d", c)
	}
	if c, o := e.call(alice, "PUT", perm, map[string]any{"subject": carol.user, "role": "commenter"}); c != 400 {
		t.Fatalf("putPerm commenter: %d %v", c, o)
	}
	if c, o := e.call(alice, "PUT", perm, map[string]any{"subject": carol.user, "role": "editor"}); c != 200 {
		t.Fatalf("raise an existing role: %d %v", c, o)
	}
	if c, o := e.call(alice, "PUT", perm, map[string]any{"subject": carol.user, "role": ""}); c != 200 {
		t.Fatalf("remove an existing role: %d %v", c, o)
	}
	if c, _ := roleOf(carol, doc4); c != 404 {
		t.Fatalf("carol after removal: %d", c)
	}

	// ---- the daily e-mail limit: sender + recipient + diagram, only when sendEmail=true, survives revoke and delete
	doc5 := newDoc(alice, title+" 5")
	mail := map[string]any{"kind": "user", "subject": erin.user, "role": "viewer", "sendEmail": true}
	_, id1 := mustInvite(alice, doc5, mail) // creating it with sendEmail takes the day's e-mail at once
	if c, o, _, _ := invite(alice, doc5, mail); c != 409 || errCode(o) != "already_pending" {
		t.Fatalf("second invitation while one waits: %d %v", c, o)
	}
	if c, o := e.call(alice, "DELETE", base(doc5)+"/"+id1, nil); c != 200 || o["state"] != "revoked" {
		t.Fatalf("revoke: %d %v", c, o)
	}
	if c, o, _, _ := invite(alice, doc5, mail); c != 429 || errCode(o) != "email_cooldown" {
		t.Fatalf("e-mail again after revoke: %d %v", c, o)
	}
	if c, o := e.call(alice, "DELETE", base(doc5)+"/"+id1, nil); c != 200 || o["state"] != "deleted" {
		t.Fatalf("hide: %d %v", c, o)
	}
	if c, o, _, _ := invite(alice, doc5, mail); c != 429 || errCode(o) != "email_cooldown" {
		t.Fatalf("e-mail again after the row is hidden: %d %v", c, o)
	}
	// without e-mail there is no limit
	noMail := map[string]any{"kind": "user", "subject": erin.user, "role": "viewer", "sendEmail": false}
	_, id2 := mustInvite(alice, doc5, noMail)
	e.call(alice, "DELETE", base(doc5)+"/"+id2, nil)
	if _, id3 := mustInvite(alice, doc5, noMail); id3 == "" {
		t.Fatal("no id")
	}
	// the gateway gives the day's e-mail back when it could not send the mail; only the service token may do that
	if c, _ := e.call(alice, "POST", "/api/v1/internal/invitations/"+id1+"/email-failed", nil); c != 403 {
		t.Fatalf("a user releases an e-mail slot: %d", c)
	}
	doc5b := newDoc(alice, title+" 5b")
	_, idB := mustInvite(alice, doc5b, mail)
	if c, o := e.rawCall(svc, "POST", "/api/v1/internal/invitations/"+idB+"/email-failed", nil); c != 200 || o["released"] != true {
		t.Fatalf("release: %d %v", c, o)
	}
	e.call(alice, "DELETE", base(doc5b)+"/"+idB, nil) // revoke
	if c, o, _, _ := invite(alice, doc5b, mail); c != 201 {
		t.Fatalf("a released slot can be used again: %d %v", c, o)
	}
	// resend: a hand-copied invitation (no reservation) can be e-mailed once a day, with a fresh token each time
	doc5c := newDoc(alice, title+" 5c")
	oldTok, idC := mustInvite(alice, doc5c, noMail)
	rc, ro := e.call(alice, "POST", base(doc5c)+"/"+idC+"/resend", nil)
	newTok, _ := ro["token"].(string)
	if rc != 200 || newTok == "" || newTok == oldTok {
		t.Fatalf("resend: %d %v", rc, ro)
	}
	if c, _ := e.call(erin, "GET", "/api/v1/invitations/"+oldTok, nil); c != 404 {
		t.Fatalf("the old link must stop working after a resend: %d", c)
	}
	if c, o := e.call(alice, "POST", base(doc5c)+"/"+idC+"/resend", nil); c != 429 || errCode(o) != "email_cooldown" {
		t.Fatalf("second resend the same day: %d %v", c, o)
	}
	// a burst of parallel requests for the same person must create ONE invitation and take ONE e-mail (no flood)
	doc5d := newDoc(alice, title+" 5d")
	res := make(chan int, 20)
	for i := 0; i < 20; i++ {
		go func() {
			b, _ := json.Marshal(mail)
			req, _ := http.NewRequest("POST", e.ts.URL+base(doc5d), bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer "+e.token(alice))
			req.Header.Set("Content-Type", "application/json")
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				res <- -1
				return
			}
			_ = r.Body.Close()
			res <- r.StatusCode
		}()
	}
	created := 0
	for i := 0; i < 20; i++ {
		if <-res == 201 {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("a burst of 20 parallel invitations created %d (want exactly 1)", created)
	}

	// ---- at most 20 live links per diagram
	doc6 := newDoc(alice, title+" 6")
	for i := 0; i < maxLinksPerDoc; i++ {
		mustInvite(alice, doc6, map[string]any{"kind": "link", "role": "viewer"})
	}
	if c, o, _, _ := invite(alice, doc6, map[string]any{"kind": "link", "role": "viewer"}); c != 409 || errCode(o) != "too_many_invitations" {
		t.Fatalf("21st link: %d %v", c, o)
	}
}
