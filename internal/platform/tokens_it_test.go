package platform

// Integration test of the per-diagram MCP tokens (ADR-0016) against the lab services; skipped unless DRAWIO_IT=1 (see platform_it_test.go).

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
)

// raw sends a request with an arbitrary bearer token and returns the status and decoded JSON.
func (e *itEnv) raw(tok, method, path string, body any) (int, map[string]any) {
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
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{"_raw": string(b)}
	}
	return res.StatusCode, out
}

func TestIT_MCPTokens(t *testing.T) {
	e := newIT(t)
	suffix := strings.ToLower(newID(4))
	alice, bob, eve := who{user: "tk-alice-" + suffix, roles: []string{"user"}}, who{user: "tk-bob-" + suffix, roles: []string{"user"}}, who{user: "tk-eve-" + suffix, roles: []string{"user"}}
	code, out := e.call(alice, "POST", "/api/v1/documents", map[string]any{"title": "token it " + suffix})
	if code != 201 {
		t.Fatalf("create doc: %d %v", code, out)
	}
	doc := out["document"].(map[string]any)["id"].(string)
	_, out = e.call(alice, "POST", "/api/v1/documents", map[string]any{"title": "other " + suffix})
	other := out["document"].(map[string]any)["id"].(string)
	if c, _ := e.call(alice, "PUT", "/api/v1/documents/"+doc+"/permissions", map[string]any{"subject": bob.user, "role": "editor"}); c != 200 {
		t.Fatalf("share: %d", c)
	}

	base := "/api/v1/documents/" + doc + "/mcp-tokens"
	// only the owner creates tokens
	if c, _ := e.call(eve, "POST", base, tokenReq{Name: "x", Role: "editor", EnableAI: true}); c != 404 {
		t.Errorf("stranger: %d, want 404", c)
	}
	if c, _ := e.call(bob, "POST", base, tokenReq{Name: "x", Role: "editor", EnableAI: true}); c != 403 {
		t.Errorf("editor: %d, want 403", c)
	}
	if c, _ := e.call(bob, "GET", base, nil); c != 403 {
		t.Errorf("editor list: %d, want 403", c)
	}
	// AI must be on (or explicitly switched on)
	if c, o := e.call(alice, "POST", base, tokenReq{Name: "Claude", Role: "editor"}); c != 409 {
		t.Errorf("AI off: %d %v, want 409", c, o)
	}
	for _, bad := range []tokenReq{{Name: "", Role: "editor"}, {Name: "a", Role: "owner"}, {Name: "a", Role: "editor", ExpiresInDays: 5000}, {Name: strings.Repeat("a", 61), Role: "editor"}} {
		bad.EnableAI = true
		if c, _ := e.call(alice, "POST", base, bad); c != 400 {
			t.Errorf("bad request %+v: %d, want 400", bad, c)
		}
	}
	c, out := e.call(alice, "POST", base, tokenReq{Name: "Claude", Role: "editor", ExpiresInDays: 30, EnableAI: true})
	if c != 201 {
		t.Fatalf("create token: %d %v", c, out)
	}
	tok := out["token"].(string)
	info := out["tokenInfo"].(map[string]any)
	if !strings.HasPrefix(tok, TokenPrefix) || info["hint"] != tok[len(tok)-4:] || info["expiresAt"] == nil {
		t.Fatalf("token / info shape: %q %v", tok, info)
	}
	// the secret is never stored or listed
	var stored int
	if err := e.srv.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM document_tokens WHERE token_hash=$1 AND document_id=$2`, hashToken(tok), doc).Scan(&stored); err != nil || stored != 1 {
		t.Errorf("hash not stored: %d %v", stored, err)
	}
	if _, o := e.call(alice, "GET", base, nil); strings.Contains(toJSON(o), tok) {
		t.Error("the list leaks the token")
	}
	if _, o := e.call(alice, "GET", "/api/v1/documents/"+doc+"/permissions", nil); o["aiPolicy"].(map[string]any)["allowed"] != true {
		t.Error("creating a token should have switched AI on")
	}

	// the MCP server resolves tokens; nobody else may ask
	svc, _ := e.mcp.Sign(identity.Claims{Subject: "mcp-resolver", Username: "mcp-resolver", Audience: Audience, ActorType: identity.ActorService}, time.Minute)
	resolve := func(token string) (int, map[string]any) {
		return e.raw(svc, "POST", "/api/v1/internal/mcp-tokens/resolve", map[string]string{"token": token})
	}
	c, out = resolve(tok)
	if c != 200 || out["documentId"] != doc || out["role"] != RoleEditor || out["owner"] != alice.user {
		t.Fatalf("resolve: %d %v", c, out)
	}
	if c, _ := e.raw(e.token(alice), "POST", "/api/v1/internal/mcp-tokens/resolve", map[string]string{"token": tok}); c != 403 {
		t.Errorf("a human must not resolve tokens: %d", c)
	}
	gwSvc, _ := e.gw.Sign(identity.Claims{Subject: "gateway-auth", Audience: Audience, ActorType: identity.ActorService}, time.Minute)
	if c, _ := e.raw(gwSvc, "POST", "/api/v1/internal/mcp-tokens/resolve", map[string]string{"token": tok}); c != 403 {
		t.Errorf("the gateway service token must not resolve tokens: %d", c)
	}
	for _, bad := range []string{"", "dsp_", TokenPrefix + strings.Repeat("A", 43), "Bearer " + tok, strings.Repeat("x", 300)} {
		if c, _ := resolve(bad); c != 401 {
			t.Errorf("resolve(%.12q): %d, want 401", bad, c)
		}
	}

	// the token-scoped AI actor (what the MCP server signs after a successful resolve)
	ai := func(role string) string {
		s, err := e.mcp.Sign(identity.Claims{Subject: "ai-claude", Username: "ai-claude", Name: "AI claude", Audience: Audience, ActorType: identity.ActorAI, OnBehalfOf: alice.user,
			Roles: []string{"user"}, Agent: "token:" + info["id"].(string), TokenID: info["id"].(string), DocScope: doc, DocRole: role}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	aiTok := ai(RoleEditor)
	if c, o := e.raw(aiTok, "GET", "/api/v1/documents", nil); c != 200 || len(o["documents"].([]any)) != 1 || o["documents"].([]any)[0].(map[string]any)["id"] != doc {
		t.Errorf("token actor list: %d %v", c, o)
	}
	if c, _ := e.raw(aiTok, "GET", "/api/v1/documents/"+doc+"/content?format=state", nil); c != 200 {
		t.Errorf("token actor reads its diagram: %d", c)
	}
	if c, _ := e.raw(aiTok, "GET", "/api/v1/documents/"+other+"/content?format=state", nil); c != 404 {
		t.Errorf("token actor on another diagram of the same owner: %d, want 404", c)
	}
	if c, _ := e.raw(aiTok, "POST", "/api/v1/documents", map[string]any{"title": "nope"}); c != 403 {
		t.Errorf("token actor creates a diagram: %d, want 403", c)
	}
	if c, _ := e.raw(aiTok, "POST", base, tokenReq{Name: "child", Role: "editor"}); c != 403 {
		t.Errorf("token actor creates a token: %d, want 403", c)
	}
	if c, _ := e.raw(aiTok, "GET", base, nil); c != 403 {
		t.Errorf("token actor lists tokens: %d, want 403", c)
	}
	if c, _ := e.raw(aiTok, "PUT", "/api/v1/documents/"+doc+"/permissions", map[string]any{"subject": "*", "role": "editor"}); c != 403 {
		t.Errorf("token actor shares: %d, want 403", c)
	}
	if c, _ := e.raw(aiTok, "DELETE", "/api/v1/documents/"+doc, nil); c != 403 {
		t.Errorf("token actor deletes: %d, want 403", c)
	}
	// a forged scope from anyone but the MCP server is ignored (the actor then has no share and sees nothing)
	forged, _ := e.signer.Sign(identity.Claims{Subject: "ai-forge", Username: "ai-forge", Audience: Audience, ActorType: identity.ActorAI, OnBehalfOf: alice.user, Roles: []string{"user"}, DocScope: doc, DocRole: RoleEditor}, time.Minute)
	if c, _ := e.raw(forged, "GET", "/api/v1/documents/"+doc+"/content?format=state", nil); c != 404 {
		t.Errorf("forged scope from another issuer: %d, want 404", c)
	}
	// a viewer token cannot edit
	if c, _ := e.raw(ai(RoleViewer), "POST", "/api/v1/documents/"+doc+"/operations", map[string]any{"baseSeq": 0, "ops": []any{}}); c == 200 {
		t.Errorf("viewer token edited: %d", c)
	}

	// revoke, expire, switch AI off
	tid := info["id"].(string)
	if c, _ := e.call(bob, "DELETE", base+"/"+tid, nil); c != 403 {
		t.Errorf("editor revokes: %d, want 403", c)
	}
	if c, _ := e.call(alice, "DELETE", base+"/"+tid, nil); c != 200 {
		t.Errorf("owner revokes: %d", c)
	}
	if c, _ := resolve(tok); c != 401 {
		t.Errorf("revoked token resolves: %d", c)
	}
	_, out = e.call(alice, "POST", base, tokenReq{Name: "short", Role: "viewer", ExpiresInDays: 1})
	tok2 := out["token"].(string)
	if c, _ := resolve(tok2); c != 200 {
		t.Fatalf("second token: %d", c)
	}
	if _, err := e.srv.store.pool.Exec(t.Context(), `UPDATE document_tokens SET expires_at=now()-interval '1 minute' WHERE token_hash=$1`, hashToken(tok2)); err != nil {
		t.Fatal(err)
	}
	if c, _ := resolve(tok2); c != 401 {
		t.Errorf("expired token resolves: %d", c)
	}
	_, out = e.call(alice, "POST", base, tokenReq{Name: "third", Role: "editor"})
	tok3 := out["token"].(string)
	if c, _ := resolve(tok3); c != 200 {
		t.Fatalf("third token: %d", c)
	}
	_, pol := e.call(alice, "GET", "/api/v1/documents/"+doc+"/permissions", nil)
	p := pol["aiPolicy"].(map[string]any)
	e.call(alice, "PUT", "/api/v1/documents/"+doc+"/ai-policy", map[string]any{"allowed": false, "maxOpsPerMin": p["maxOpsPerMin"], "maxDelete": p["maxDelete"]})
	if c, _ := resolve(tok3); c != 401 {
		t.Errorf("token resolves although AI is off: %d", c)
	}
	// the list keeps the history, revoked ones included
	if _, o := e.call(alice, "GET", base, nil); len(o["tokens"].([]any)) != 3 {
		t.Errorf("token history: %v", o)
	}
	// deleting the diagram kills its tokens
	e.call(alice, "PUT", "/api/v1/documents/"+doc+"/ai-policy", map[string]any{"allowed": true, "maxOpsPerMin": p["maxOpsPerMin"], "maxDelete": p["maxDelete"]})
	if c, _ := e.call(alice, "DELETE", "/api/v1/documents/"+doc, nil); c != 200 {
		t.Fatalf("delete doc: %d", c)
	}
	if c, _ := resolve(tok3); c != 401 {
		t.Errorf("token of a deleted diagram resolves: %d", c)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
