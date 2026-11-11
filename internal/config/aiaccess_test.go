package config

import "testing"

func TestAIAccess(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		role    string
		user    []string
		want    bool
	}{
		{"switch off beats everything", false, "", []string{"mcp-access", "drawio-admin"}, false},
		{"switch off, no role set", false, "mcp-access", []string{"mcp-access"}, false},
		{"no role required: any signed-in user", true, "", nil, true},
		{"role required and held", true, "mcp-access", []string{"user", "mcp-access"}, true},
		{"role required, not held", true, "mcp-access", []string{"user"}, false},
		{"role required, user has no roles at all", true, "mcp-access", nil, false},
		{"administrator gets no shortcut", true, "mcp-access", []string{"admin", "drawio-admin"}, false},
		{"role names are exact (no prefix/case match)", true, "mcp-access", []string{"mcp-access-readonly", "MCP-ACCESS"}, false},
	}
	for _, c := range cases {
		if got := AIAccess(c.enabled, c.role, c.user); got != c.want {
			t.Errorf("%s: AIAccess(%v,%q,%v) = %v, want %v", c.name, c.enabled, c.role, c.user, got, c.want)
		}
	}
}

func TestAIEnvIsReadByGatewayAndPlatform(t *testing.T) {
	g, err := LoadGateway(env(map[string]string{"AI_ENABLED": "true", "AI_ACCESS_ROLE": "  mcp-access "}))
	if err != nil {
		t.Fatal(err)
	}
	if !g.AIEnabled || g.AIAccessRole != "mcp-access" {
		t.Fatalf("gateway: enabled=%v role=%q", g.AIEnabled, g.AIAccessRole)
	}
	g, err = LoadGateway(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if g.AIEnabled || g.AIAccessRole != "" {
		t.Fatalf("default must be off and without a role: enabled=%v role=%q", g.AIEnabled, g.AIAccessRole)
	}
}
