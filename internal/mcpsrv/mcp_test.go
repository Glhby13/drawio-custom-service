package mcpsrv

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
)

type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, tok string) (*Principal, error) {
	switch tok {
	case "good":
		return &Principal{Subject: "u1", Username: "alice", Azp: "drawio-mcp-claude", Roles: []string{"user"}}, nil
	case "norole":
		return &Principal{Subject: "u2", Username: "bob", Azp: "drawio-mcp-claude"}, nil
	}
	return nil, errors.New("bad token")
}

func setup(t *testing.T) (*httptest.Server, *[]string, *identity.Verifier) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ver := identity.NewVerifier(pub, "mcp-1", "drawio-mcp")
	var seen []string
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		c, err := ver.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "platform")
		if err != nil || c.ActorType != identity.ActorAI || c.OnBehalfOf != "alice" || c.Username != "ai-claude" {
			http.Error(w, "bad internal token", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/content") && r.URL.Query().Get("format") == "state":
			_, _ = w.Write([]byte(`{"seq":1,"state":[{"id":"p1","name":"P","cells":[{"id":"0","k":"-","p":"","o":"","props":{}},{"id":"1","k":"-","p":"0","o":"1","props":{}}]}]}`))
		case strings.HasSuffix(r.URL.Path, "/operations"):
			_, _ = w.Write([]byte(`{"seqs":[2]}`))
		case strings.HasSuffix(r.URL.Path, "/missing/content"):
			http.Error(w, `{"error":{"code":"not_found"}}`, http.StatusNotFound)
		default:
			_, _ = w.Write([]byte(`{"documents":[{"id":"d1","title":"T","owner":"alice","role":"editor","headSeq":3}]}`))
		}
	}))
	t.Cleanup(plat.Close)
	s := New(Config{PublicURL: "https://x.test", IssuerURL: "https://kc.test/realms/lab", PlatformURL: plat.URL, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier: fakeVerifier{}, Signer: identity.NewSigner(priv, "mcp-1", "drawio-mcp")})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, &seen, ver
}

func rpc(t *testing.T, base, tok, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestAuthAndMetadata(t *testing.T) {
	srv, _, _ := setup(t)
	st, _, h := rpc(t, srv.URL, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if st != 401 || !strings.Contains(h.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("no token: %d %q", st, h.Get("WWW-Authenticate"))
	}
	if st, _, _ := rpc(t, srv.URL, "forged", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); st != 401 {
		t.Fatalf("forged token: %d", st)
	}
	if st, _, _ := rpc(t, srv.URL, "norole", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); st != 403 {
		t.Fatalf("user without the application role: %d", st)
	}
	resp, _ := http.Get(srv.URL + "/.well-known/oauth-protected-resource")
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if m["resource"] != "https://x.test/mcp" || m["authorization_servers"].([]any)[0] != "https://kc.test/realms/lab" {
		t.Fatalf("metadata %v", m)
	}
}

func TestToolsActAsDelegatedAI(t *testing.T) {
	srv, seen, _ := setup(t)
	_, out, _ := rpc(t, srv.URL, "good", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if !strings.Contains(out, "add_shape") || !strings.Contains(out, "apply_operations") {
		t.Fatalf("tools: %s", out)
	}
	_, out, _ = rpc(t, srv.URL, "good", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add_shape","arguments":{"document":"d1","label":"Hi","x":10,"y":20,"style":"rounded=1;fillColor=#fff;"}}}`)
	if !strings.Contains(out, "Added shape") {
		t.Fatalf("add_shape: %s", out)
	}
	last := (*seen)[len(*seen)-1]
	for _, want := range []string{`"t":"addCell"`, `"parent":"1"`, `"st:fillColor":"#fff"`, `"v":"Hi"`, `"gx":"10"`} {
		if !strings.Contains(last, want) {
			t.Errorf("operation lacks %s: %s", want, last)
		}
	}
	_, out, _ = rpc(t, srv.URL, "good", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_diagram","arguments":{"document":"missing"}}}`)
	if !strings.Contains(out, `"isError":true`) || !strings.Contains(out, "not found") {
		t.Fatalf("missing diagram must be a tool error the agent can read: %s", out)
	}
	_, out, _ = rpc(t, srv.URL, "good", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	if !strings.Contains(out, "-32602") {
		t.Fatalf("unknown tool: %s", out)
	}
	_, out, _ = rpc(t, srv.URL, "good", `{"jsonrpc":"2.0","id":5,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if !strings.Contains(out, `"protocolVersion":"2025-03-26"`) {
		t.Fatalf("initialize: %s", out)
	}
	if st, body, _ := rpc(t, srv.URL, "good", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); st != 202 || body != "" {
		t.Fatalf("notification: %d %q", st, body)
	}
}

func TestAgentName(t *testing.T) {
	for in, want := range map[string]string{"drawio-mcp-claude": "ai-claude", "drawio-mcp": "ai-agent", "My Agent_2": "ai-my-agent-2"} {
		if got := agentName(in); got != want {
			t.Errorf("agentName(%q)=%q want %q", in, got, want)
		}
	}
}
