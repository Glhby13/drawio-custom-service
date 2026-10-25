package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func pair(t *testing.T) (*Signer, *Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return NewSigner(priv, "k1", "gateway"), NewVerifier(pub, "k1", "gateway")
}

func TestRoundTrip(t *testing.T) {
	s, v := pair(t)
	tok, err := s.Sign(Claims{Subject: "u1", Audience: "platform", ActorType: ActorHuman, Roles: []string{"user"}, SessionID: "s"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := v.Verify(tok, "platform")
	if err != nil || c.Subject != "u1" || c.ActorType != ActorHuman || c.JTI == "" {
		t.Fatalf("verify: %+v %v", c, err)
	}
}

func TestRejections(t *testing.T) {
	s, v := pair(t)
	tok, _ := s.Sign(Claims{Subject: "u1", Audience: "platform", ActorType: ActorAI, OnBehalfOf: "u2"}, time.Minute)
	_, other := pair(t)
	parts := strings.Split(tok, ".")
	cases := map[string]struct {
		token, aud string
		v          *Verifier
	}{
		"wrong audience":  {tok, "mcp", v},
		"wrong key":       {tok, "platform", other},
		"tampered claims": {parts[0] + "." + parts[1] + "A." + parts[2], "platform", v},
		"garbage":         {"a.b", "platform", v},
		"empty":           {"", "platform", v},
		"alg none":        {"eyJhbGciOiJub25lIiwidHlwIjoiSldUIiwia2lkIjoiazEifQ." + parts[1] + ".", "platform", v},
	}
	for name, c := range cases {
		if _, err := c.v.Verify(c.token, c.aud); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestExpiryAndTTLCap(t *testing.T) {
	s, v := pair(t)
	if _, err := s.Sign(Claims{Subject: "u", Audience: "a", ActorType: ActorHuman}, 6*time.Minute); err == nil {
		t.Fatal("ttl above the 5 minute cap must be refused")
	}
	if _, err := s.Sign(Claims{Subject: "u", Audience: "a", ActorType: "root"}, time.Minute); err == nil {
		t.Fatal("invalid actorType must be refused")
	}
	tok, _ := s.Sign(Claims{Subject: "u", Audience: "a", ActorType: ActorService}, time.Minute)
	v.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := v.Verify(tok, "a"); err == nil {
		t.Fatal("expired token accepted")
	}
}
