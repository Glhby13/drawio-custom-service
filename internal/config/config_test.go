package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func env(over map[string]string) func(string) string {
	m := map[string]string{
		"PUBLIC_URL": "https://drawio.lab.local", "ISSUER_URL": "https://auth.lab.local/realms/lab/", "OIDC_CLIENT_ID": "drawio-lab",
		"OIDC_CLIENT_SECRET": "x", "DRAWIO_UPSTREAM": "http://drawio:8080", "REDIS_ADDR": "redis:6379",
		"INTERNAL_KEY_SEED": base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)),
	}
	for k, v := range over {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func TestValid(t *testing.T) {
	g, err := LoadGateway(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if g.IssuerURL != "https://auth.lab.local/realms/lab" || g.RequiredRole != "user" || g.Port != "8080" || len(g.InternalKey) != ed25519.PrivateKeySize {
		t.Fatalf("%+v", g)
	}
}

func TestRefusesUnsafeOrIncomplete(t *testing.T) {
	cases := map[string]map[string]string{
		"http issuer":                {"ISSUER_URL": "http://auth.lab.local/realms/lab"},
		"http public url":            {"PUBLIC_URL": "http://drawio.lab.local"},
		"insecure cookie with https": {"INSECURE_COOKIE": "true"},
		"missing secret":             {"OIDC_CLIENT_SECRET": ""},
		"missing redis":              {"REDIS_ADDR": ""},
		"bad upstream":               {"DRAWIO_UPSTREAM": "not a url"},
		"short signing seed":         {"INTERNAL_KEY_SEED": base64.StdEncoding.EncodeToString([]byte("short"))},
		"missing signing seed":       {"INTERNAL_KEY_SEED": ""},
		"non-positive session ttl":   {"SESSION_TTL": "0s"},
		"unparsable idle":            {"SESSION_IDLE": "soon"},
	}
	for name, over := range cases {
		if _, err := LoadGateway(env(over)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := LoadGateway(env(map[string]string{"PUBLIC_URL": "http://localhost:8080", "INSECURE_COOKIE": "true"})); err != nil {
		t.Errorf("local plain-http test mode: %v", err)
	}
}

func TestErrorsNeverEchoSecrets(t *testing.T) {
	_, err := LoadGateway(env(map[string]string{"OIDC_CLIENT_SECRET": "super-secret-value", "INTERNAL_KEY_SEED": "bm90LTMyLWJ5dGVz", "ISSUER_URL": "http://x"}))
	if err == nil || strings.Contains(err.Error(), "super-secret-value") || strings.Contains(err.Error(), "bm90LTMyLWJ5dGVz") {
		t.Fatalf("error leaks a secret: %v", err)
	}
}
