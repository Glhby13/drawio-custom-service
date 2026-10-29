package platform

import (
	"context"
	"strings"
	"testing"

	"github.com/Glhby13/drawio-custom-service/internal/model"
)

func TestNewMCPToken(t *testing.T) {
	a, ha, err := newMCPToken()
	if err != nil {
		t.Fatal(err)
	}
	b, hb, _ := newMCPToken()
	if !strings.HasPrefix(a, TokenPrefix) || len(a) < len(TokenPrefix)+40 {
		t.Errorf("token shape: %q", a)
	}
	if a == b || ha == hb {
		t.Error("tokens must be unique")
	}
	if hashToken(a) != ha || strings.Contains(ha, a) || len(ha) != 64 {
		t.Error("hash must be the stable 64-hex SHA-256 of the token")
	}
}

// docRole for a per-diagram token: this diagram only, at most the token's role, capped at editor, only while AI is allowed
// and while the token's delegator still owns the diagram. No store is needed for these decisions.
func TestDocRoleToken(t *testing.T) {
	s := &Server{}
	ctx := context.Background()
	doc := &DocMeta{ID: "dOne", Owner: "alice", AI: AIPolicy{Allowed: true}}
	tok := func(role string) *Principal {
		return &Principal{Username: "ai-x", IsAI: true, TokenID: "t1", TokenDoc: "dOne", TokenRole: role, Actor: model.Actor{OnBehalfOf: "alice"}}
	}
	for _, tc := range []struct {
		name string
		p    *Principal
		d    *DocMeta
		want string
	}{
		{"viewer token", tok(RoleViewer), doc, RoleViewer},
		{"commenter token", tok(RoleCommenter), doc, RoleCommenter},
		{"editor token", tok(RoleEditor), doc, RoleEditor},
		{"owner role is capped at editor", tok(RoleOwner), doc, RoleEditor},
		{"other diagram", tok(RoleEditor), &DocMeta{ID: "dTwo", Owner: "alice", AI: AIPolicy{Allowed: true}}, RoleNone},
		{"AI switched off", tok(RoleEditor), &DocMeta{ID: "dOne", Owner: "alice"}, RoleNone},
		{"owner changed", tok(RoleEditor), &DocMeta{ID: "dOne", Owner: "bob", AI: AIPolicy{Allowed: true}}, RoleNone},
	} {
		got, err := s.docRole(ctx, tc.p, tc.d)
		if err != nil || got != tc.want {
			t.Errorf("%s: role %q (err %v), want %q", tc.name, got, err, tc.want)
		}
	}
	// a token actor can never share, set policy, delete or audit (AI actors never can)
	for _, a := range []Action{ActShare, ActDelete, ActPolicy, ActAudit} {
		if s.can(tok(RoleEditor), RoleEditor, a) {
			t.Errorf("token actor must not be able to %s", a)
		}
	}
}
