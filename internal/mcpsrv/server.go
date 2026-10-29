// Package mcpsrv is the MCP server of drawio-custom-service (ADR-0008/0009): a second edge, next to the gateway, that lets
// AI agents work on diagrams through the same platform API and authorisation as people. It speaks MCP over Streamable
// HTTP (JSON-RPC 2.0 on POST /mcp), authenticates agents with a per-diagram access token ("dsp_…", ADR-0016) or, when
// enabled, an OAuth 2.1 bearer token issued by Keycloak, and calls the platform with a short-lived internal token whose
// actor is an AI delegated by the diagram's owner (token) or by the signed-in user (OAuth).
package mcpsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
)

// Principal is the verified OAuth caller.
type Principal struct {
	Subject  string
	Username string // the human the agent acts for (Keycloak preferred_username)
	Name     string
	Azp      string // OAuth client id of the agent application
	Roles    []string
}

// Verifier validates a bearer access token (signature, issuer, expiry, audience).
type Verifier interface {
	Verify(ctx context.Context, token string) (*Principal, error)
}

// Config of the server.
type Config struct {
	PublicURL    string // https://drawio.lab.local
	IssuerURL    string
	PlatformURL  string
	RequiredRole string
	Log          *slog.Logger
	Verifier     Verifier // nil = OAuth is off: only per-diagram tokens are accepted
	Signer       *identity.Signer
	HTTP         *http.Client
}

// Server is the HTTP handler.
type Server struct {
	cfg Config
	mux *http.ServeMux
}

// New builds the server.
func New(cfg Config) *Server {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.RequiredRole == "" {
		cfg.RequiredRole = "user"
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ready")) })
	if cfg.Verifier != nil {
		s.mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.metadata)
		s.mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.metadata)
	}
	s.mux.HandleFunc("POST /mcp", s.rpc)
	s.mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "this server does not offer a server-initiated stream", http.StatusMethodNotAllowed)
	})
	s.mux.HandleFunc("DELETE /mcp", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return s
}

// Handler returns the handler.
func (s *Server) Handler() http.Handler { return s.mux }

// metadata is the OAuth 2.0 Protected Resource Metadata (RFC 9728) that tells agents which authorization server to use.
func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":                 s.cfg.PublicURL + "/mcp",
		"authorization_servers":    []string{s.cfg.IssuerURL},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"openid", "profile"},
		"resource_name":            "Diagrams (draw.io) MCP server",
	})
}

func (s *Server) unauthorized(w http.ResponseWriter, why string) {
	if s.cfg.Verifier == nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", error="invalid_token", error_description=%q`, s.cfg.PublicURL, why))
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

var nonName = regexp.MustCompile(`[^a-z0-9]+`)

// agentName derives the AI actor name from the OAuth client id ("drawio-mcp-claude" -> "ai-claude").
func agentName(azp string) string {
	n := strings.Trim(nonName.ReplaceAllString(strings.ToLower(strings.TrimPrefix(strings.ToLower(azp), "drawio-mcp")), "-"), "-")
	if n == "" {
		n = "agent"
	}
	if len(n) > 40 {
		n = n[:40]
	}
	return "ai-" + n
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

func (s *Server) rpc(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		s.unauthorized(w, "bearer token required")
		return
	}
	var ac actor
	if strings.HasPrefix(tok, tokenPrefix) {
		ti, err := s.resolveToken(r.Context(), tok)
		switch {
		case errors.Is(err, errTokenRejected):
			s.cfg.Log.Info("mcp token rejected")
			s.unauthorized(w, "token not accepted")
			return
		case err != nil:
			s.cfg.Log.Error("mcp token lookup failed", "err", err.Error())
			http.Error(w, "token check unavailable, retry shortly", http.StatusServiceUnavailable)
			return
		}
		ac = s.actorForToken(ti, r)
	} else if s.cfg.Verifier != nil {
		p, err := s.cfg.Verifier.Verify(r.Context(), tok)
		if err != nil {
			s.cfg.Log.Info("mcp token rejected", "err", err.Error())
			s.unauthorized(w, "token not accepted")
			return
		}
		hasRole := false
		for _, role := range p.Roles {
			if role == s.cfg.RequiredRole || role == "admin" {
				hasRole = true
			}
		}
		if !hasRole {
			http.Error(w, "the signed-in user has no access to Diagrams", http.StatusForbidden)
			return
		}
		ac = s.actorFor(p, r)
	} else {
		s.unauthorized(w, "token not accepted")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' { // batching is not offered
		writeRPC(w, rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{Code: -32600, Message: "batch requests are not supported"}})
		return
	}
	var req rpcReq
	if json.Unmarshal(body, &req) != nil || req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{Code: -32700, Message: "invalid JSON-RPC request"}})
		return
	}
	if len(req.ID) == 0 { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	res, rerr := s.dispatch(r.Context(), ac, req)
	resp := rpcResp{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr}
	if rerr != nil {
		resp.Result = nil
	}
	writeRPC(w, resp)
}

func writeRPC(w http.ResponseWriter, resp rpcResp) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// actor is the AI identity a request acts as.
type actor struct {
	Name, Display, OnBehalfOf, Agent, Model string
	// per-diagram token scope (empty for OAuth callers)
	TokenID, DocScope, DocRole string
}

func (s *Server) actorFor(p *Principal, r *http.Request) actor {
	name := agentName(p.Azp)
	model := strings.TrimSpace(r.Header.Get("X-Agent-Model"))
	if len(model) > 64 {
		model = model[:64]
	}
	return actor{Name: name, Display: "AI " + strings.TrimPrefix(name, "ai-"), OnBehalfOf: p.Username, Agent: p.Azp, Model: model}
}

const protocolVersion = "2025-06-18"

func (s *Server) dispatch(ctx context.Context, a actor, req rpcReq) (any, *rpcErr) {
	switch req.Method {
	case "initialize":
		var in struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &in)
		v := protocolVersion
		if in.ProtocolVersion == "2025-03-26" || in.ProtocolVersion == "2024-11-05" || in.ProtocolVersion == protocolVersion {
			v = in.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}, "resources": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "drawio-custom-service", "version": "1.0.0"},
			"instructions":    "Tools to read and edit shared draw.io diagrams. You act as an AI collaborator on behalf of the signed-in user: you only see diagrams that allow AI and that were shared with you, and your edits are attributed, rate-limited and revertible.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefsFor(a)}, nil
	case "tools/call":
		var in struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &in) != nil || in.Name == "" {
			return nil, &rpcErr{Code: -32602, Message: "name is required"}
		}
		text, err := s.callTool(ctx, a, in.Name, in.Arguments)
		if err != nil {
			var ue *toolError
			if errors.As(err, &ue) {
				return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": ue.Error()}}}, nil
			}
			if errors.Is(err, errUnknownTool) {
				return nil, &rpcErr{Code: -32602, Message: "unknown tool " + in.Name}
			}
			s.cfg.Log.Error("tool failed", "tool", in.Name, "err", err.Error())
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "internal error"}}}, nil
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}, nil
	case "resources/list":
		return s.listResources(ctx, a)
	case "resources/read":
		return s.readResource(ctx, a, req.Params)
	default:
		return nil, &rpcErr{Code: -32601, Message: "method not found"}
	}
}
