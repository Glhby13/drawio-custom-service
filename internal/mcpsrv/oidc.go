package mcpsrv

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// KeycloakVerifier validates agent access tokens: signature (JWKS), issuer, expiry and the audience of this MCP server.
// The Keycloak client role of the application (client id `AppClient`) decides whether the user may use Diagrams at all.
type KeycloakVerifier struct {
	issuer, audience, appClient string
	client                      *http.Client
	mu                          sync.Mutex
	v                           *oidc.IDTokenVerifier
}

// NewKeycloakVerifier builds it; pool adds the lab CA to the system roots.
func NewKeycloakVerifier(issuer, audience, appClient string, pool *x509.CertPool, tlsMin uint16) *KeycloakVerifier {
	tr := &http.Transport{TLSClientConfig: tlsCfg(pool, tlsMin), Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	return &KeycloakVerifier{issuer: issuer, audience: audience, appClient: appClient, client: &http.Client{Transport: tr, Timeout: 15 * time.Second}}
}

func (k *KeycloakVerifier) ensure() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.v != nil {
		return nil
	}
	p, err := oidc.NewProvider(oidc.ClientContext(context.Background(), k.client), k.issuer)
	if err != nil {
		return fmt.Errorf("oidc discovery: %w", err)
	}
	k.v = p.Verifier(&oidc.Config{SkipClientIDCheck: true})
	return nil
}

// Verify implements Verifier.
func (k *KeycloakVerifier) Verify(ctx context.Context, token string) (*Principal, error) {
	if err := k.ensure(); err != nil {
		return nil, err
	}
	t, err := k.v.Verify(oidc.ClientContext(ctx, k.client), token)
	if err != nil {
		return nil, err
	}
	var c struct {
		Aud            any    `json:"aud"`
		Azp            string `json:"azp"`
		Username       string `json:"preferred_username"`
		Name           string `json:"name"`
		ResourceAccess map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
	}
	if err := t.Claims(&c); err != nil {
		return nil, err
	}
	hasAud := false
	for _, a := range t.Audience {
		if a == k.audience {
			hasAud = true
		}
	}
	if !hasAud {
		return nil, errors.New("token audience is not this MCP server")
	}
	if c.Username == "" || c.Azp == "" {
		return nil, errors.New("token lacks preferred_username/azp")
	}
	return &Principal{Subject: t.Subject, Username: c.Username, Name: c.Name, Azp: c.Azp, Roles: c.ResourceAccess[k.appClient].Roles}, nil
}
