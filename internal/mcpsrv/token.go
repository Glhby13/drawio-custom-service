package mcpsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
)

// Per-diagram access tokens (ADR-0016). The MCP server holds no database credentials (ADR-0008): it asks the platform what
// a "dsp_" token is, on every request, so a revoked or expired token stops working immediately.

const tokenPrefix = "dsp_"

var errTokenRejected = errors.New("token rejected")

// tokenInfo is what the platform tells about a live token.
type tokenInfo struct {
	TokenID    string `json:"tokenId"`
	Name       string `json:"name"`
	DocumentID string `json:"documentId"`
	Role       string `json:"role"`
	Owner      string `json:"owner"`
}

func (s *Server) resolveToken(ctx context.Context, token string) (*tokenInfo, error) {
	svc, err := s.cfg.Signer.Sign(identity.Claims{Subject: "mcp-resolver", Username: "mcp-resolver", Audience: "platform", ActorType: identity.ActorService}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(map[string]string{"token": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.PlatformURL, "/")+"/api/v1/internal/mcp-tokens/resolve", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+svc)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		var ti tokenInfo
		if json.Unmarshal(body, &ti) != nil || ti.DocumentID == "" || ti.Owner == "" {
			return nil, errors.New("platform answered an unreadable token record")
		}
		return &ti, nil
	case http.StatusUnauthorized:
		return nil, errTokenRejected
	default:
		return nil, fmt.Errorf("platform answered %d", resp.StatusCode)
	}
}

// actorForToken: the AI acts on behalf of the diagram's owner and can only reach that diagram.
func (s *Server) actorForToken(ti *tokenInfo, r *http.Request) actor {
	name := tokenActorName(ti.Name)
	model := strings.TrimSpace(r.Header.Get("X-Agent-Model"))
	if len(model) > 64 {
		model = model[:64]
	}
	return actor{Name: name, Display: "AI " + strings.TrimPrefix(name, "ai-"), OnBehalfOf: ti.Owner, Agent: "token:" + ti.TokenID, Model: model,
		TokenID: ti.TokenID, DocScope: ti.DocumentID, DocRole: ti.Role}
}

// tokenActorName turns a token label ("Claude Desktop") into an actor name ("ai-claude-desktop").
func tokenActorName(label string) string {
	n := strings.Trim(nonName.ReplaceAllString(strings.ToLower(label), "-"), "-")
	if n == "" {
		n = "agent"
	}
	if len(n) > 40 {
		n = strings.Trim(n[:40], "-")
	}
	return "ai-" + n
}
