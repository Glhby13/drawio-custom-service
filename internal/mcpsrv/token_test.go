package mcpsrv

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
)

// tokenSetup runs an MCP server in token-only mode (no Verifier) against a fake platform.
func tokenSetup(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ver := identity.NewVerifier(pub, "mcp-1", "drawio-mcp")
	var seen []string
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := ver.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "platform")
		if err != nil {
			http.Error(w, "bad internal token", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/api/v1/internal/mcp-tokens/resolve" {
			if c.ActorType != identity.ActorService || c.Subject != "mcp-resolver" {
				http.Error(w, "not the resolver", http.StatusForbidden)
				return
			}
			var in struct{ Token string }
			_ = json.Unmarshal(body, &in)
			switch in.Token {
			case "dsp_good":
				_, _ = w.Write([]byte(`{"tokenId":"t1","name":"Claude Desktop","documentId":"dOne","role":"editor","owner":"alice"}`))
			case "dsp_down":
				http.Error(w, "boom", http.StatusInternalServerError)
			default:
				http.Error(w, `{"error":{"code":"invalid_token"}}`, http.StatusUnauthorized)
			}
			return
		}
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" as="+c.Username+" obo="+c.OnBehalfOf+" scope="+c.DocScope+"/"+c.DocRole+"/"+c.TokenID+" agent="+c.Agent)
		_, _ = w.Write([]byte(`{"documents":[{"id":"dOne","title":"T","owner":"alice","role":"editor","headSeq":3}]}`))
	}))
	t.Cleanup(plat.Close)
	s := New(Config{PublicURL: "https://x.test", PlatformURL: plat.URL, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Signer: identity.NewSigner(priv, "mcp-1", "drawio-mcp")})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, &seen
}

func rpcCall(t *testing.T, base, tok, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestTokenAuth(t *testing.T) {
	srv, _ := tokenSetup(t)
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	for _, tc := range []struct {
		name, tok string
		want      int
	}{
		{"no header", "", 401}, {"unknown dsp token", "dsp_nope", 401}, {"oauth token while OAuth is off", "some.jwt.value", 401},
		{"platform down", "dsp_down", 503}, {"good", "dsp_good", 200},
	} {
		resp, _ := rpcCall(t, srv.URL, tc.tok, ping)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
		if tc.want == 401 && strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata") {
			t.Errorf("%s: token-only mode must not advertise OAuth metadata", tc.name)
		}
	}
	r, _ := http.Get(srv.URL + "/.well-known/oauth-protected-resource")
	if r.StatusCode != 404 {
		t.Errorf("OAuth metadata should be absent, got %d", r.StatusCode)
	}
	_ = r.Body.Close()
}

func TestTokenScope(t *testing.T) {
	srv, seen := tokenSetup(t)
	// the catalogue hides create_diagram and makes "document" optional
	_, out := rpcCall(t, srv.URL, "dsp_good", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if strings.Contains(out, "create_diagram") {
		t.Error("create_diagram must not be offered to a per-diagram token")
	}
	var tl struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Required []string `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	_ = json.Unmarshal([]byte(out), &tl)
	for _, tool := range tl.Result.Tools {
		for _, r := range tool.InputSchema.Required {
			if r == "document" {
				t.Errorf("%s still requires document", tool.Name)
			}
		}
	}
	// create_diagram is refused even when called by name
	_, out = rpcCall(t, srv.URL, "dsp_good", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_diagram","arguments":{"title":"x"}}}`)
	if !strings.Contains(out, "cannot create") {
		t.Errorf("create_diagram answer: %s", out)
	}
	// another diagram is refused before any platform call
	before := len(*seen)
	_, out = rpcCall(t, srv.URL, "dsp_good", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_diagram","arguments":{"document":"dOther"}}}`)
	if !strings.Contains(out, "only gives access to diagram dOne") || len(*seen) != before {
		t.Errorf("foreign diagram not refused (%d calls): %s", len(*seen)-before, out)
	}
	// the platform sees the scope, the owner as delegator and the token as agent
	_, _ = rpcCall(t, srv.URL, "dsp_good", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_diagrams","arguments":{}}}`)
	last := (*seen)[len(*seen)-1]
	for _, want := range []string{"as=ai-claude-desktop", "obo=alice", "scope=dOne/editor/t1", "agent=token:t1"} {
		if !strings.Contains(last, want) {
			t.Errorf("platform call %q lacks %q", last, want)
		}
	}
}

func TestTokenActorName(t *testing.T) {
	for in, want := range map[string]string{"Claude Desktop": "ai-claude-desktop", "  ": "ai-agent", "Ünï!!": "ai-n", "a/b\\c": "ai-a-b-c"} {
		if got := tokenActorName(in); got != want {
			t.Errorf("tokenActorName(%q) = %q, want %q", in, got, want)
		}
	}
}
