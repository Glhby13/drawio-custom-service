package platform

// Integration test against the lab PostgreSQL / SeaweedFS S3 / Redis (through ssh tunnels). Skipped unless DRAWIO_IT=1.
// Env: DRAWIO_IT_PG (host:port), DRAWIO_IT_S3 (host:port), DRAWIO_IT_REDIS (host:port) and the matching *_PASSWORD / *_SECRET.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/Glhby13/drawio-custom-service/internal/config"
	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/model"
)

type itEnv struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	signer *identity.Signer
	gw     *identity.Signer // same key, issuer "drawio-gateway": what the real gateway signs (service tokens, local sign-ins)
}

func newIT(t *testing.T) *itEnv {
	if os.Getenv("DRAWIO_IT") != "1" {
		t.Skip("DRAWIO_IT=1 not set")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	hp := strings.Split(os.Getenv("DRAWIO_IT_PG"), ":")
	cfg := config.Platform{Port: "0", PGHost: hp[0], PGPort: hp[1], PGDB: "drawio_test", PGUser: "lab_worker", PGPassword: os.Getenv("DRAWIO_IT_PG_PASSWORD"), PGSSLMode: "disable",
		S3Endpoint: os.Getenv("DRAWIO_IT_S3"), S3Bucket: "drawio-documents", S3AccessKey: "drawio", S3SecretKey: os.Getenv("DRAWIO_IT_S3_SECRET"), S3Region: "us-east-1",
		RedisAddr: os.Getenv("DRAWIO_IT_REDIS"), RedisUser: "drawio", RedisPassword: os.Getenv("DRAWIO_IT_REDIS_PASSWORD"),
		Trusted: []config.TrustedKey{{Kid: "t-1", Pub: pub, Issuer: "it-edge"}, {Kid: "gw-1", Pub: pub, Issuer: "drawio-gateway"}}, SnapshotEvery: 10, AdminRole: "admin", UserRole: "user", AIDefaultMaxOpsPerMin: 300, AIDefaultMaxDelete: 20}
	ctx := context.Background()
	pool, err := OpenPG(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	blob, err := NewBlob(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Username: cfg.RedisUser, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = rdb.Close() })
	srv := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), NewStore(pool), blob, rdb)
	cctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	srv.Start(cctx)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &itEnv{t: t, srv: srv, ts: ts, signer: identity.NewSigner(priv, "t-1", "it-edge"), gw: identity.NewSigner(priv, "gw-1", "drawio-gateway")}
}

type who struct {
	user  string
	roles []string
	typ   string
	obo   string
}

func (e *itEnv) token(w who) string {
	typ := w.typ
	if typ == "" {
		typ = identity.ActorHuman
	}
	tok, err := e.signer.Sign(identity.Claims{Subject: "sub-" + w.user, Audience: Audience, ActorType: typ, Username: w.user, Name: strings.ToUpper(w.user), Roles: w.roles, OnBehalfOf: w.obo, Agent: map[bool]string{true: "test-agent", false: ""}[typ == "ai"], Model: map[bool]string{true: "test-model", false: ""}[typ == "ai"]}, time.Minute)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *itEnv) call(w who, method, path string, body any) (int, map[string]any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+e.token(w))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{"_raw": string(raw)}
	}
	return res.StatusCode, out
}

type wsConn struct {
	c    *websocket.Conn
	in   chan Out
	e    *itEnv
	name string
}

func (e *itEnv) dial(w who, docID string, since int64) (*wsConn, int) {
	h := http.Header{"Authorization": []string{"Bearer " + e.token(w)}}
	c, res, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(e.ts.URL, "http")+"/ws/v1/documents/"+docID, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		code := 0
		if res != nil {
			code = res.StatusCode
		}
		return nil, code
	}
	c.SetReadLimit(16 << 20)
	wc := &wsConn{c: c, in: make(chan Out, 4096), e: e, name: w.user}
	go func() {
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				close(wc.in)
				return
			}
			var o Out
			_ = json.Unmarshal(data, &o)
			wc.in <- o
		}
	}()
	b, _ := json.Marshal(map[string]any{"t": "join", "since": since})
	_ = c.Write(context.Background(), websocket.MessageText, b)
	e.t.Cleanup(func() { _ = c.CloseNow() })
	return wc, 101
}

func (w *wsConn) send(v any) {
	b, _ := json.Marshal(v)
	if err := w.c.Write(context.Background(), websocket.MessageText, b); err != nil {
		w.e.t.Fatalf("%s write: %v", w.name, err)
	}
}

// next returns the next message of type t (ignoring others) within the timeout.
func (w *wsConn) next(t string, d time.Duration) (Out, bool) {
	tm := time.After(d)
	for {
		select {
		case o, ok := <-w.in:
			if !ok {
				return Out{}, false
			}
			if o.T == t {
				return o, true
			}
		case <-tm:
			return Out{}, false
		}
	}
}

func (e *itEnv) hashOf(w who, docID string) (string, float64) {
	code, out := e.call(w, "GET", "/api/v1/documents/"+docID+"/content?format=state", nil)
	if code != 200 {
		e.t.Fatalf("state: %d %v", code, out)
	}
	return out["hash"].(string), out["seq"].(float64)
}

func addVertex(id, cell, parent, order string) model.Op {
	return model.Op{ID: id, T: model.OpAddCell, Page: "p1", Cell: cell, Kind: "v", Parent: parent, Order: order,
		Props: map[string]string{"gx": "10", "gy": "20", "gw": "80", "gh": "40", "v": cell}}
}

func TestPlatformEndToEnd(t *testing.T) {
	e := newIT(t)
	alice := who{user: "alice", roles: []string{"user"}}
	bob := who{user: "bob", roles: []string{"user"}}
	carol := who{user: "carol", roles: []string{"user"}}
	admin := who{user: "boss", roles: []string{"user", "admin"}}
	ai := who{user: "ai-1", roles: []string{"user"}, typ: "ai", obo: "alice"}
	tag := fmt.Sprint(time.Now().UnixNano() % 1e9)

	// ---- documents + sharing
	code, out := e.call(alice, "POST", "/api/v1/documents", map[string]any{"title": "IT " + tag})
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	docID := out["document"].(map[string]any)["id"].(string)
	if c, _ := e.call(bob, "GET", "/api/v1/documents/"+docID, nil); c != 404 {
		t.Fatalf("bob must not see the document (404, no existence oracle), got %d", c)
	}
	if c, _ := e.call(bob, "PUT", "/api/v1/documents/"+docID+"/permissions", map[string]any{"subject": "bob", "role": "editor"}); c != 404 {
		t.Fatalf("bob must not be able to share: %d", c)
	}
	for _, p := range []map[string]any{{"subject": "bob", "role": "editor"}, {"subject": "carol", "role": "viewer"}} {
		if c, o := e.call(alice, "PUT", "/api/v1/documents/"+docID+"/permissions", p); c != 200 {
			t.Fatalf("share: %d %v", c, o)
		}
	}
	if c, o := e.call(admin, "GET", "/api/v1/documents/"+docID, nil); c != 200 || o["role"] != "" {
		t.Fatalf("admin read: %d role=%v", c, o["role"])
	}
	_, list := e.call(bob, "GET", "/api/v1/documents", nil)
	if n := len(list["documents"].([]any)); n < 1 {
		t.Fatalf("bob's list is empty")
	}

	// ---- realtime: two editors converge
	wa, st := e.dial(alice, docID, 0)
	if wa == nil {
		t.Fatalf("alice ws: %d", st)
	}
	wb, _ := e.dial(bob, docID, 0)
	wc, _ := e.dial(carol, docID, 0)
	for _, w := range []*wsConn{wa, wb, wc} {
		j, ok := w.next("joined", 5*time.Second)
		if !ok || j.Mode != "full" || len(j.State) == 0 || j.XML == "" {
			t.Fatalf("%s: no full join (%v)", w.name, ok)
		}
	}
	if p, ok := wa.next("presence", 5*time.Second); !ok || len(p.Presence) < 1 {
		t.Fatalf("no presence")
	}
	wa.send(In{T: "ops", Batch: "b1", Ops: []model.Op{addVertex("a1", "n1", "1", "m")}})
	if _, ok := wa.next("ack", 5*time.Second); !ok {
		t.Fatal("no ack for alice")
	}
	for _, w := range []*wsConn{wb, wc} {
		o, ok := w.next("op", 5*time.Second)
		if !ok || o.Op.Cell != "n1" || o.Op.Actor.ID != "alice" || o.Op.Res == nil {
			t.Fatalf("%s: did not receive alice's resolved op: %+v", w.name, o)
		}
	}
	// concurrent writers on the same cell/key: both get sequenced, last wins, the earlier author is told
	v1, v2 := "from-alice", "from-bob"
	wa.send(In{T: "ops", Batch: "b2", Base: 1, Ops: []model.Op{{ID: "a2", T: model.OpSetProp, Page: "p1", Cell: "n1", Key: "v", Val: &v1}}})
	wb.send(In{T: "ops", Batch: "b3", Base: 1, Ops: []model.Op{{ID: "b1", T: model.OpSetProp, Page: "p1", Cell: "n1", Key: "v", Val: &v2}}})
	if _, ok := wa.next("ack", 5*time.Second); !ok {
		t.Fatal("alice ack b2")
	}
	if _, ok := wb.next("ack", 5*time.Second); !ok {
		t.Fatal("bob ack b3")
	}
	time.Sleep(300 * time.Millisecond)
	ha, _ := e.hashOf(alice, docID)
	hb, _ := e.hashOf(bob, docID)
	if ha != hb {
		t.Fatal("hash differs between readers")
	}
	// the viewer cannot write; the rejection is explicit
	wc.send(In{T: "ops", Batch: "bx", Ops: []model.Op{addVertex("c1", "evil", "1", "n")}})
	if n, ok := wc.next("nack", 5*time.Second); !ok || n.Code != "forbidden" {
		t.Fatalf("viewer write must be nacked forbidden, got %+v %v", n, ok)
	}
	// invalid op: explicit nack, document unchanged
	wa.send(In{T: "ops", Batch: "bad", Ops: []model.Op{{ID: "bad1", T: model.OpSetProp, Page: "p1", Cell: "ghost", Key: "v", Val: &v1}}})
	if n, ok := wa.next("nack", 5*time.Second); !ok || n.Code != "cell_gone" {
		t.Fatalf("expected cell_gone, got %+v", n)
	}
	// idempotency: the same batch again (lost ack) is acknowledged but not applied twice
	_, before := e.hashOf(alice, docID)
	wa.send(In{T: "ops", Batch: "b1", Ops: []model.Op{addVertex("a1", "n1", "1", "m")}})
	if a, ok := wa.next("ack", 5*time.Second); !ok || len(a.Seqs) != 1 {
		t.Fatalf("duplicate must be acked with the original seq: %+v", a)
	}
	if _, after := e.hashOf(alice, docID); after != before {
		t.Fatal("duplicate batch advanced the document")
	}

	// ---- revoke: bob loses access immediately
	if c, _ := e.call(alice, "PUT", "/api/v1/documents/"+docID+"/permissions", map[string]any{"subject": "bob", "role": ""}); c != 200 {
		t.Fatal("unshare failed")
	}
	if _, ok := wb.next("never", 2*time.Second); ok {
		t.Fatal("unexpected")
	}
	if c, _ := e.call(bob, "GET", "/api/v1/documents/"+docID, nil); c != 404 {
		t.Fatalf("bob after revoke: %d", c)
	}
	if w, c := e.dial(bob, docID, 0); w != nil || c != 404 {
		t.Fatalf("bob ws after revoke: %v %d", w != nil, c)
	}

	// ---- AI: disabled by default, then enabled with its own share, limits enforced, attributed, revertable
	if c, _ := e.call(ai, "GET", "/api/v1/documents/"+docID, nil); c != 404 {
		t.Fatalf("AI must not see a document without AI policy + its own share: %d", c)
	}
	e.call(alice, "PUT", "/api/v1/documents/"+docID+"/ai-policy", map[string]any{"allowed": true, "maxOpsPerMin": 300, "maxDelete": 2})
	if c, _ := e.call(ai, "GET", "/api/v1/documents/"+docID, nil); c != 404 {
		t.Fatalf("AI without its own share must still see nothing: %d", c)
	}
	e.call(alice, "PUT", "/api/v1/documents/"+docID+"/permissions", map[string]any{"subject": "ai-1", "role": "editor"})
	if c, o := e.call(ai, "POST", "/api/v1/documents/"+docID+"/operations", map[string]any{"batch": "ai-b1", "ops": []model.Op{addVertex("ai1", "c-ai", "1", "q"), addVertex("ai2", "c-ai2", "c-ai", "m")}}); c != 200 {
		t.Fatalf("AI ops: %d %v", c, o)
	}
	if c, _ := e.call(ai, "PUT", "/api/v1/documents/"+docID+"/permissions", map[string]any{"subject": "ai-1", "role": "editor"}); c != 404 && c != 403 {
		t.Fatalf("AI must not manage sharing: %d", c)
	}
	// a batch that deletes more than maxDelete cells is refused (needs a human)
	big := []model.Op{addVertex("g0", "grp", "1", "r")}
	for i := 0; i < 4; i++ {
		big = append(big, addVertex(fmt.Sprintf("g%d", i+1), fmt.Sprintf("k%d", i), "grp", fmt.Sprintf("%c", 'a'+i)))
	}
	if c, o := e.call(alice, "POST", "/api/v1/documents/"+docID+"/operations", map[string]any{"batch": "big", "ops": big}); c != 200 {
		t.Fatalf("alice big: %d %v", c, o)
	}
	if c, o := e.call(ai, "POST", "/api/v1/documents/"+docID+"/operations", map[string]any{"batch": "ai-del", "ops": []model.Op{{ID: "d1", T: model.OpDeleteCell, Page: "p1", Cell: "grp"}}}); c != 409 {
		t.Fatalf("AI mass delete must need confirmation (409), got %d %v", c, o)
	}
	if c, _ := e.call(alice, "POST", "/api/v1/documents/"+docID+"/operations", map[string]any{"batch": "human-del", "ops": []model.Op{{ID: "d2", T: model.OpDeleteCell, Page: "p1", Cell: "grp"}}}); c != 200 {
		t.Fatal("a human may delete the group")
	}
	// history attributes the AI batch; revert restores the state before it
	_, hist := e.call(alice, "GET", "/api/v1/documents/"+docID+"/history?limit=50", nil)
	foundAI := false
	for _, h := range hist["history"].([]any) {
		m := h.(map[string]any)
		if m["batch"] == "ai-b1" && m["actorType"] == "ai" && m["onBehalfOf"] == "alice" && m["agent"] == "test-agent" {
			foundAI = true
		}
	}
	if !foundAI {
		t.Fatalf("AI batch is not attributed in the history: %v", hist)
	}
	if c, o := e.call(alice, "POST", "/api/v1/documents/"+docID+"/revert", map[string]any{"batch": "ai-b1"}); c != 200 || o["applied"].(float64) < 1 {
		t.Fatalf("revert: %d %v", c, o)
	}
	_, cont := e.call(alice, "GET", "/api/v1/documents/"+docID+"/content?format=model", nil)
	if strings.Contains(fmt.Sprint(cont), "c-ai") {
		t.Fatalf("AI cells should be gone after revert: %v", cont)
	}

	// ---- versions + restore (resync for connected clients)
	_, ver := e.call(alice, "POST", "/api/v1/documents/"+docID+"/versions", map[string]any{"name": "v-" + tag})
	vid := ver["version"].(map[string]any)["id"].(string)
	hashAtVersion, _ := e.hashOf(alice, docID)
	wa.send(In{T: "ops", Batch: "b-after", Ops: []model.Op{addVertex("z1", "after", "1", "s")}})
	if _, ok := wa.next("ack", 5*time.Second); !ok {
		t.Fatal("ack after version")
	}
	if c, o := e.call(alice, "POST", "/api/v1/documents/"+docID+"/versions/"+vid+"/restore", nil); c != 200 {
		t.Fatalf("restore: %d %v", c, o)
	}
	if _, ok := wa.next("resync", 5*time.Second); !ok {
		t.Fatal("restore must tell sessions to resync")
	}
	if h, _ := e.hashOf(alice, docID); h != hashAtVersion {
		t.Fatal("restore did not reproduce the version state")
	}

	// ---- many ops -> snapshots happen; a cold cache reloads the identical state (S3 snapshot + log tail)
	for i := 0; i < 30; i++ {
		wa.send(In{T: "ops", Batch: fmt.Sprintf("m%d", i), Ops: []model.Op{addVertex(fmt.Sprintf("m%d", i), fmt.Sprintf("mc%d", i), "1", "t"+string(rune('a'+i%26)))}})
		if _, ok := wa.next("ack", 5*time.Second); !ok {
			t.Fatalf("ack m%d", i)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	live, seq := e.hashOf(alice, docID)
	e.srv.hub.mu.Lock()
	e.srv.hub.docs = map[string]*docHandle{}
	e.srv.hub.mu.Unlock()
	cold, seq2 := e.hashOf(alice, docID)
	if live != cold || seq != seq2 {
		t.Fatalf("cold reload differs: seq %v/%v", seq, seq2)
	}
	_, hist2 := e.call(alice, "GET", "/api/v1/documents/"+docID+"/history?limit=5", nil)
	if len(hist2["history"].([]any)) == 0 {
		t.Fatal("empty history")
	}

	// ---- audit is owner/admin only, and records denials
	if c, _ := e.call(carol, "GET", "/api/v1/documents/"+docID+"/audit", nil); c != 403 {
		t.Fatalf("viewer must not read the audit: %d", c)
	}
	if c, o := e.call(alice, "GET", "/api/v1/documents/"+docID+"/audit", nil); c != 200 || len(o["events"].([]any)) < 3 {
		t.Fatalf("audit: %d %v", c, o)
	}
	// ---- delete
	if c, _ := e.call(alice, "DELETE", "/api/v1/documents/"+docID, nil); c != 200 {
		t.Fatal("delete")
	}
	if c, _ := e.call(alice, "GET", "/api/v1/documents/"+docID, nil); c != 404 {
		t.Fatal("deleted document still visible")
	}
}

func TestUnauthenticatedAndForgedTokensAreRejected(t *testing.T) {
	e := newIT(t)
	for name, h := range map[string]string{"none": "", "garbage": "Bearer abc.def.ghi", "wrong audience": "Bearer " + func() string {
		tok, _ := e.signer.Sign(identity.Claims{Subject: "x", Audience: "other", ActorType: "human", Roles: []string{"user"}}, time.Minute)
		return tok
	}()} {
		req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/documents", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != 401 {
			t.Fatalf("%s: expected 401, got %v %v", name, res, err)
		}
	}
	// a valid token without the drawio role is refused
	if c, _ := e.call(who{user: "nobody", roles: []string{"other"}}, "GET", "/api/v1/documents", nil); c != 403 {
		t.Fatalf("no role: %d", c)
	}
}

func TestLivePresence(t *testing.T) {
	e := newIT(t)
	alice := who{user: "alice", roles: []string{"user"}}
	bob := who{user: "bob", roles: []string{"user"}}
	carol := who{user: "carol", roles: []string{"user"}}
	dave := who{user: "dave", roles: []string{"user"}}
	ai := who{user: "ai-1", roles: []string{"user"}, typ: "ai", obo: "alice"}
	_, out := e.call(alice, "POST", "/api/v1/documents", map[string]any{"title": "presence"})
	docID := out["document"].(map[string]any)["id"].(string)
	for _, p := range []map[string]any{{"subject": "bob", "role": "editor"}, {"subject": "carol", "role": "viewer"}, {"subject": "ai-1", "role": "editor"}} {
		e.call(alice, "PUT", "/api/v1/documents/"+docID+"/permissions", p)
	}
	e.call(alice, "PUT", "/api/v1/documents/"+docID+"/ai-policy", map[string]any{"allowed": true, "maxOpsPerMin": 100, "maxDelete": 5})
	wa, _ := e.dial(alice, docID, 0)
	wb, _ := e.dial(bob, docID, 0)
	wc, _ := e.dial(carol, docID, 0)
	for _, w := range []*wsConn{wa, wb, wc} {
		if _, ok := w.next("joined", 5*time.Second); !ok {
			t.Fatal("join")
		}
	}
	if w, code := e.dial(dave, docID, 0); w != nil || code != 404 {
		t.Fatalf("a user without access must not even open the socket (404), got %d", code)
	}
	seq0 := func() float64 { _, s := e.hashOf(alice, docID); return s }()

	// selection reaches the others, never the sender; identity comes from the token, not from the message
	wa.send(map[string]any{"t": "act", "act": map[string]any{"state": "selecting", "page": "p1", "cells": []string{"1"}, "user": "mallory", "sid": "forged", "type": "ai"}})
	for _, w := range []*wsConn{wb, wc} {
		o, ok := w.next("act", 3*time.Second)
		if !ok || o.Act.User != "alice" || o.Act.State != ActSelecting || o.Act.Type != "human" || o.Act.SID == "forged" || len(o.Act.Cells) != 1 {
			t.Fatalf("%s: selection %+v ok=%v", w.name, o.Act, ok)
		}
		if ttl := o.Act.Exp - time.Now().UnixMilli(); ttl < 5000 || ttl > 9000 {
			t.Fatalf("expiry %d ms from now", ttl)
		}
	}
	if _, ok := wa.next("act", 300*time.Millisecond); ok {
		t.Fatal("the sender must not receive its own activity")
	}

	// editing with a draft: editor ok; a viewer's "editing" is shown as a selection only
	d := strings.Repeat("x", 900)
	wb.send(map[string]any{"t": "act", "act": map[string]any{"state": "editing", "page": "p1", "cells": []string{"1"}, "draft": d}})
	o, ok := wa.next("act", 3*time.Second)
	if !ok || o.Act.State != ActEditing || o.Act.Draft == nil || len([]rune(*o.Act.Draft)) != maxDraftRune {
		t.Fatalf("editing draft: %+v", o.Act)
	}
	wc.send(map[string]any{"t": "act", "act": map[string]any{"state": "editing", "page": "p1", "cells": []string{"1"}, "draft": "viewer typing"}})
	for {
		o, ok = wa.next("act", 3*time.Second)
		if !ok {
			t.Fatal("no activity from the viewer")
		}
		if o.Act.User == "carol" {
			break
		}
	}
	if o.Act.State != ActSelecting || o.Act.Draft != nil {
		t.Fatalf("a viewer can only select: %+v", o.Act)
	}

	// drag preview + idle
	wb.send(map[string]any{"t": "act", "act": map[string]any{"state": "dragging", "page": "p1", "cells": []string{"1"}, "preview": map[string]any{"dx": 12.5, "dy": -3}}})
	for {
		o, ok = wa.next("act", 3*time.Second)
		if !ok {
			t.Fatal("no drag")
		}
		if o.Act.User == "bob" && o.Act.State == ActDragging {
			break
		}
	}
	if o.Act.Preview == nil || *o.Act.Preview.Dx != 12.5 {
		t.Fatalf("preview %+v", o.Act.Preview)
	}
	wb.send(map[string]any{"t": "act", "act": map[string]any{"state": "idle"}})
	for {
		o, ok = wa.next("act", 3*time.Second)
		if !ok {
			t.Fatal("no idle")
		}
		if o.Act.User == "bob" && o.Act.State == ActIdle {
			break
		}
	}

	// a late joiner sees what is active now (alice's selection), expired/idle entries are not replayed
	wl, _ := e.dial(bob, docID, 0)
	if _, ok := wl.next("joined", 5*time.Second); !ok {
		t.Fatal("late join")
	}
	var snap Out
	for { // the snapshot lists every live collaborator in map order: look for alice's entry
		var ok bool
		if snap, ok = wl.next("act", 3*time.Second); !ok {
			t.Fatal("alice's selection is missing from the join snapshot")
		}
		if snap.Act.User == "alice" {
			break
		}
	}
	if snap.Act.State != ActSelecting {
		t.Fatalf("snapshot %+v", snap.Act)
	}

	// the AI works through REST: it shows up as "applying" with the cells it touches, only while it does
	code, _ := e.call(ai, "POST", "/api/v1/documents/"+docID+"/operations", map[string]any{"ops": []model.Op{{ID: "ai-op-1", T: model.OpAddCell, Page: "p1", Cell: "ai-cell", Kind: "v", Parent: "1", Props: map[string]string{"gx": "1", "gy": "1", "gw": "10", "gh": "10", "v": "x"}}}})
	if code != 200 {
		t.Fatalf("ai ops: %d", code)
	}
	for {
		o, ok = wa.next("act", 3*time.Second)
		if !ok {
			t.Fatal("no AI activity")
		}
		if o.Act.Type == "ai" {
			break
		}
	}
	if o.Act.State != ActApplying || len(o.Act.Cells) != 1 || o.Act.Cells[0] != "ai-cell" || o.Act.User != "ai-1" {
		t.Fatalf("ai activity %+v", o.Act)
	}

	// leaving removes the entry
	_ = wb.c.Close(websocket.StatusNormalClosure, "bye")
	for {
		o, ok = wa.next("act", 4*time.Second)
		if !ok {
			t.Fatal("no gone")
		}
		if o.Act.State == ActGone {
			break
		}
	}

	// burst: rate limited (best effort) but the server stays healthy and idle always passes
	for i := 0; i < 300; i++ {
		wc.send(map[string]any{"t": "act", "act": map[string]any{"state": "selecting", "page": "p1", "cells": []string{"1"}}})
	}
	wc.send(map[string]any{"t": "act", "act": map[string]any{"state": "idle"}})
	got, idle := 0, false
	for !idle {
		o, ok = wa.next("act", 3*time.Second)
		if !ok {
			t.Fatal("idle after burst never arrived")
		}
		if o.Act.User == "carol" {
			got++
			idle = o.Act.State == ActIdle
		}
	}
	if got >= 250 {
		t.Fatalf("burst not limited: %d of 301 delivered", got)
	}

	// presence is not document state: the op log did not move because of it (only the AI's own op did)
	_, seq1 := e.hashOf(alice, docID)
	if seq1 != seq0+1 {
		t.Fatalf("presence must not write operations: seq %v -> %v", seq0, seq1)
	}
}

// ---- local accounts --------------------------------------------------------------------------------------------------------

func (e *itEnv) localTok(user string) string {
	tok, err := e.gw.Sign(identity.Claims{Subject: "local:" + user, Audience: Audience, ActorType: identity.ActorHuman, Username: user, Name: user, Idp: "local"}, time.Minute)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *itEnv) rawCall(tok, method, path string, body any) (int, map[string]any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

func TestLocalAccounts(t *testing.T) {
	e := newIT(t)
	tag := fmt.Sprint(time.Now().UnixNano() % 1e8)
	admin := who{user: "boss", roles: []string{"admin"}}
	plain := who{user: "plainuser", roles: []string{"user"}}
	lu, la := "lus"+tag, "las"+tag
	svc, _ := e.gw.Sign(identity.Claims{Subject: "gateway-auth", Audience: Audience, ActorType: identity.ActorService}, 30*time.Second)
	verify := func(user, pw string) (int, map[string]any) {
		return e.rawCall(svc, "POST", "/api/v1/internal/auth/local", map[string]string{"username": user, "password": pw})
	}
	t.Cleanup(func() {
		for _, u := range []string{lu, la} {
			e.call(admin, "DELETE", "/api/v1/admin/local-users/"+u, nil)
		}
	})

	// only administrators manage accounts
	if c, _ := e.call(plain, "GET", "/api/v1/admin/local-users", nil); c != 403 {
		t.Fatalf("a normal user must not list local accounts: %d", c)
	}
	if c, _ := e.call(plain, "POST", "/api/v1/admin/local-users", map[string]any{"username": lu, "role": "user", "password": "a long enough password"}); c != 403 {
		t.Fatalf("a normal user must not create accounts: %d", c)
	}
	// validation
	for name, body := range map[string]map[string]any{
		"short password":  {"username": lu, "role": "user", "password": "short"},
		"bad username":    {"username": "Bad Name!", "role": "user", "password": "a long enough password"},
		"bad role":        {"username": lu, "role": "root", "password": "a long enough password"},
		"password = name": {"username": lu, "role": "user", "password": strings.ToUpper(lu)},
	} {
		if c, _ := e.call(admin, "POST", "/api/v1/admin/local-users", body); c != 400 {
			t.Fatalf("%s accepted: %d", name, c)
		}
	}
	if c, o := e.call(admin, "POST", "/api/v1/admin/local-users", map[string]any{"username": lu, "name": "Local User", "role": "user", "password": "a long enough password"}); c != 201 {
		t.Fatalf("create: %d %v", c, o)
	}
	if c, _ := e.call(admin, "POST", "/api/v1/admin/local-users", map[string]any{"username": lu, "role": "user", "password": "a long enough password"}); c != 409 {
		t.Fatalf("duplicate username: %d", c)
	}
	// an SSO account that already exists blocks the name
	e.call(who{user: "sso" + tag, roles: []string{"user"}}, "GET", "/api/v1/documents", nil)
	time.Sleep(400 * time.Millisecond) // the principal is remembered asynchronously
	if c, _ := e.call(admin, "POST", "/api/v1/admin/local-users", map[string]any{"username": "sso" + tag, "role": "user", "password": "a long enough password"}); c != 409 {
		t.Fatalf("local username equal to an SSO account: %d", c)
	}

	// the credential check (service token only)
	if c, o := verify(lu, "a long enough password"); c != 200 || o["role"] != "user" || o["sub"] != "local:"+lu {
		t.Fatalf("verify: %d %v", c, o)
	}
	for _, bad := range [][2]string{{lu, "wrong password!!"}, {"nobody" + tag, "a long enough password"}} {
		if c, _ := verify(bad[0], bad[1]); c != 401 {
			t.Fatalf("bad credentials accepted: %v %d", bad, c)
		}
	}
	if c, _ := e.rawCall(e.token(admin), "POST", "/api/v1/internal/auth/local", map[string]string{"username": lu, "password": "a long enough password"}); c != 403 {
		t.Fatalf("a browser-style token must not use the credential check: %d", c)
	}
	mcpTok, _ := e.signer.Sign(identity.Claims{Subject: "gateway-auth", Audience: Audience, ActorType: identity.ActorService}, 30*time.Second) // wrong issuer
	if c, _ := e.rawCall(mcpTok, "POST", "/api/v1/internal/auth/local", map[string]string{"username": lu, "password": "a long enough password"}); c != 403 {
		t.Fatalf("only the gateway issuer may call it: %d", c)
	}

	// a local account is an ordinary principal: owns diagrams, shares with SSO users
	code, out := e.rawCall(e.localTok(lu), "POST", "/api/v1/documents", map[string]any{"title": "local owner"})
	if code != 201 {
		t.Fatalf("local user creates a diagram: %d %v", code, out)
	}
	doc := out["document"].(map[string]any)
	docID := doc["id"].(string)
	if doc["owner"] != lu {
		t.Fatalf("owner %v", doc["owner"])
	}
	bob := who{user: "bob" + tag, roles: []string{"user"}}
	if c, _ := e.rawCall(e.localTok(lu), "PUT", "/api/v1/documents/"+docID+"/permissions", map[string]any{"subject": bob.user, "role": "editor"}); c != 200 {
		t.Fatalf("share to an SSO user: %d", c)
	}
	if c, o := e.call(bob, "GET", "/api/v1/documents/"+docID, nil); c != 200 || o["role"] != "editor" {
		t.Fatalf("the SSO user sees the local user's diagram: %d %v", c, o["role"])
	}
	// ... and the other way round
	_, o2 := e.call(bob, "POST", "/api/v1/documents", map[string]any{"title": "sso owner"})
	d2 := o2["document"].(map[string]any)["id"].(string)
	e.call(bob, "PUT", "/api/v1/documents/"+d2+"/permissions", map[string]any{"subject": lu, "role": "viewer"})
	if c, o := e.rawCall(e.localTok(lu), "GET", "/api/v1/documents/"+d2, nil); c != 200 || o["role"] != "viewer" {
		t.Fatalf("the local user sees the SSO user's diagram: %d %v", c, o["role"])
	}
	// a token that claims to be local but is not from the gateway is refused; so is an SSO token borrowing a local name
	forged, _ := e.signer.Sign(identity.Claims{Subject: "local:" + lu, Audience: Audience, ActorType: identity.ActorHuman, Username: lu, Idp: "local"}, time.Minute)
	if c, _ := e.rawCall(forged, "GET", "/api/v1/documents", nil); c != 403 {
		t.Fatalf("a local claim from another issuer: %d", c)
	}
	if c, _ := e.call(who{user: lu, roles: []string{"user"}}, "GET", "/api/v1/documents", nil); c != 403 {
		t.Fatalf("an SSO token must not take over a local username: %d", c)
	}
	// a local account has no roles in its token: role comes from the database. Promote -> admin powers; demote -> gone
	if c, _ := e.rawCall(e.localTok(lu), "GET", "/api/v1/admin/local-users", nil); c != 403 {
		t.Fatalf("a local user is not an admin: %d", c)
	}
	if c, o := e.call(admin, "PATCH", "/api/v1/admin/local-users/"+lu, map[string]any{"role": "admin"}); c != 200 {
		t.Fatalf("promote: %d %v", c, o)
	}
	if c, _ := e.rawCall(e.localTok(lu), "GET", "/api/v1/admin/local-users", nil); c != 200 {
		t.Fatalf("a local admin lists accounts: %d", c)
	}
	e.call(admin, "PATCH", "/api/v1/admin/local-users/"+lu, map[string]any{"role": "user"})
	// own password
	if c, _ := e.rawCall(e.localTok(lu), "POST", "/api/v1/me/password", map[string]string{"current": "WRONG wrong wrong", "new": "another long password"}); c != 403 {
		t.Fatalf("wrong current password: %d", c)
	}
	if c, _ := e.rawCall(e.localTok(lu), "POST", "/api/v1/me/password", map[string]string{"current": "a long enough password", "new": "another long password"}); c != 200 {
		t.Fatalf("change password: %d", c)
	}
	if c, _ := verify(lu, "a long enough password"); c != 401 {
		t.Fatalf("the old password still works: %d", c)
	}
	if c, _ := verify(lu, "another long password"); c != 200 {
		t.Fatalf("the new password does not work: %d", c)
	}
	if c, _ := e.call(bob, "POST", "/api/v1/me/password", map[string]string{"current": "x", "new": "another long password"}); c != 400 {
		t.Fatalf("an SSO account has no local password: %d", c)
	}
	// admin reset, then disable: sign-in and live tokens stop working
	e.call(admin, "PATCH", "/api/v1/admin/local-users/"+lu, map[string]any{"password": "reset by the admin"})
	if c, _ := verify(lu, "reset by the admin"); c != 200 {
		t.Fatalf("admin reset: %d", c)
	}
	if c, o := e.call(admin, "PATCH", "/api/v1/admin/local-users/"+lu, map[string]any{"disabled": true}); c != 200 {
		t.Fatalf("disable: %d %v", c, o)
	}
	if c, _ := verify(lu, "reset by the admin"); c != 401 {
		t.Fatalf("disabled account can sign in: %d", c)
	}
	if c, _ := e.rawCall(e.localTok(lu), "GET", "/api/v1/documents", nil); c != 403 {
		t.Fatalf("a disabled account keeps working with an old session: %d", c)
	}
	// cannot delete an account that owns diagrams; deleting the diagram first allows it (and removes its shares)
	e.call(admin, "PATCH", "/api/v1/admin/local-users/"+lu, map[string]any{"disabled": false})
	if c, _ := e.call(admin, "DELETE", "/api/v1/admin/local-users/"+lu, nil); c != 409 {
		t.Fatalf("delete while owning diagrams: %d", c)
	}
	e.rawCall(e.localTok(lu), "DELETE", "/api/v1/documents/"+docID, nil)
	if c, o := e.call(admin, "DELETE", "/api/v1/admin/local-users/"+lu, nil); c != 200 {
		t.Fatalf("delete: %d %v", c, o)
	}
	if c, _ := verify(lu, "reset by the admin"); c != 401 {
		t.Fatalf("deleted account can sign in: %d", c)
	}
}
