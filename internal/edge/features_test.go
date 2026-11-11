package edge

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Glhby13/drawio-custom-service/internal/config"
	"github.com/Glhby13/drawio-custom-service/internal/session"
)

// GET /api/me tells the UI whether to show the AI menu (ADR-0022): global switch AND the Keycloak role of the AI access group.
func TestMeFeaturesAI(t *testing.T) {
	cases := []struct {
		name  string
		cfg   config.Gateway
		roles []string
		want  bool
	}{
		{"switch off", config.Gateway{AIAccessRole: "mcp-access"}, []string{"user", "mcp-access"}, false},
		{"member of the AI group", config.Gateway{AIEnabled: true, AIAccessRole: "mcp-access"}, []string{"user", "mcp-access"}, true},
		{"not a member", config.Gateway{AIEnabled: true, AIAccessRole: "mcp-access"}, []string{"user"}, false},
		{"administrator without the role", config.Gateway{AIEnabled: true, AIAccessRole: "mcp-access"}, []string{"user", "drawio-admin"}, false},
		{"no role configured (lab)", config.Gateway{AIEnabled: true}, []string{"user"}, true},
	}
	for _, c := range cases {
		s := &Server{cfg: c.cfg}
		w := httptest.NewRecorder()
		s.me(w, httptest.NewRequest("GET", "/api/me", nil), &session.Session{Username: "alice", Roles: c.roles})
		var out struct {
			Features map[string]bool `json:"features"`
		}
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if out.Features["ai"] != c.want {
			t.Errorf("%s: features.ai = %v, want %v", c.name, out.Features["ai"], c.want)
		}
	}
}
