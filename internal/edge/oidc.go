package edge

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Identity is what the gateway keeps from a verified login.
type Identity struct {
	Subject  string
	Username string
	Name     string
	Email    string
	Roles    []string
	IDToken  string
}

// OIDC talks to Keycloak. Both the ID token and the access token are verified against the issuer's JWKS
// (signature, iss, exp, nonce, azp); there is no InsecureSkipVerify and no unverified token is ever trusted.
type OIDC struct {
	issuer, clientID, secret, redirect string
	client                             *http.Client
	mu                                 sync.Mutex
	prov                               *oidc.Provider
	idv, accv                          *oidc.IDTokenVerifier
	endSession                         string
}

// NewOIDC builds the client; pool adds the lab CA to the system roots.
func NewOIDC(issuer, clientID, secret, redirect string, pool *x509.CertPool) *OIDC {
	tr := &http.Transport{TLSClientConfig: tlsConfig(pool), Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	return &OIDC{issuer: issuer, clientID: clientID, secret: secret, redirect: redirect, client: &http.Client{Transport: tr, Timeout: 15 * time.Second}}
}

// ensure runs discovery lazily so the gateway starts even if Keycloak is briefly unreachable
// (readiness reports it); a later request retries.
func (o *OIDC) ensure() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.prov != nil {
		return nil
	}
	// The provider keeps this context for JWKS refreshes, so it must outlive any request.
	ctx := oidc.ClientContext(context.Background(), o.client)
	p, err := oidc.NewProvider(ctx, o.issuer)
	if err != nil {
		return fmt.Errorf("oidc discovery: %w", err)
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := p.Claims(&meta); err != nil {
		return fmt.Errorf("oidc metadata: %w", err)
	}
	o.prov = p
	o.endSession = meta.EndSession
	o.idv = p.Verifier(&oidc.Config{ClientID: o.clientID})
	o.accv = p.Verifier(&oidc.Config{SkipClientIDCheck: true})
	return nil
}

// Ready reports whether discovery has succeeded.
func (o *OIDC) Ready() error { return o.ensure() }

func (o *OIDC) oauth() *oauth2.Config {
	return &oauth2.Config{ClientID: o.clientID, ClientSecret: o.secret, Endpoint: o.prov.Endpoint(), RedirectURL: o.redirect, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
}

// AuthURL builds the authorization request with PKCE (S256).
func (o *OIDC) AuthURL(state, nonce, verifier string) (string, error) {
	if err := o.ensure(); err != nil {
		return "", err
	}
	return o.oauth().AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// ErrNoRole is returned when the token carries no client role at all.
var ErrNoRole = errors.New("no client roles")

// ErrBadCredentials is returned when Keycloak refuses the username or password.
var ErrBadCredentials = errors.New("invalid credentials")

// ErrTOTPRequired is returned when Keycloak requires a one-time code.
var ErrTOTPRequired = errors.New("totp required")

// DirectGrant authenticates directly with Keycloak using username and password (Resource Owner Password Credentials).
func (o *OIDC) DirectGrant(ctx context.Context, username, password, totp string) (*Identity, error) {
	if err := o.ensure(); err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":    {"password"},
		"client_id":     {o.clientID},
		"client_secret": {o.secret},
		"username":      {username},
		"password":      {password},
		"scope":         {"openid profile email"},
	}
	if totp != "" {
		form.Set("totp", totp)
		form.Set("otp", totp)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.prov.Endpoint().TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("direct grant: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &errResp)
		if errResp.Error == "invalid_grant" {
			desc := strings.ToLower(errResp.Description)
			if strings.Contains(desc, "totp") || strings.Contains(desc, "otp") || strings.Contains(desc, "authenticator") {
				return nil, ErrTOTPRequired
			}
			return nil, ErrBadCredentials
		}
		return nil, fmt.Errorf("direct grant failed (%d): %s", resp.StatusCode, errResp.Description)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("direct grant response: %w", err)
	}
	if tok.IDToken == "" {
		return nil, errors.New("no id_token in direct grant response")
	}

	return o.verifyTokens(ctx, tok.IDToken, tok.AccessToken, "")
}

// Exchange trades the code for tokens and verifies everything.
func (o *OIDC) Exchange(ctx context.Context, code, verifier, nonce string) (*Identity, error) {
	if err := o.ensure(); err != nil {
		return nil, err
	}
	ctx = context.WithValue(oidc.ClientContext(ctx, o.client), oauth2.HTTPClient, o.client)
	tok, err := o.oauth().Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("code exchange: %w", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, errors.New("no id_token")
	}
	return o.verifyTokens(ctx, raw, tok.AccessToken, nonce)
}

func (o *OIDC) verifyTokens(ctx context.Context, rawIDToken, rawAccessToken, nonce string) (*Identity, error) {
	idt, err := o.idv.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("id token: %w", err)
	}
	if nonce != "" && idt.Nonce != nonce {
		return nil, errors.New("nonce mismatch")
	}
	var idc struct {
		Username string `json:"preferred_username"`
		Name     string `json:"name"`
		Email    string `json:"email"`
	}
	if err := idt.Claims(&idc); err != nil {
		return nil, err
	}
	at, err := o.accv.Verify(ctx, rawAccessToken)
	if err != nil {
		return nil, fmt.Errorf("access token: %w", err)
	}
	var ac struct {
		Azp            string `json:"azp"`
		ResourceAccess map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
	}
	if err := at.Claims(&ac); err != nil {
		return nil, err
	}
	if ac.Azp != o.clientID || at.Subject != idt.Subject {
		return nil, errors.New("access token does not belong to this client/user")
	}
	return &Identity{Subject: idt.Subject, Username: idc.Username, Name: idc.Name, Email: idc.Email, Roles: ac.ResourceAccess[o.clientID].Roles, IDToken: rawIDToken}, nil
}

// EndSessionURL is the RP-initiated logout URL (ends the Keycloak SSO session too).
func (o *OIDC) EndSessionURL(idToken, postLogout string) (string, error) {
	if err := o.ensure(); err != nil {
		return "", err
	}
	if o.endSession == "" {
		return "", errors.New("issuer has no end_session_endpoint")
	}
	q := url.Values{"id_token_hint": {idToken}, "post_logout_redirect_uri": {postLogout}, "client_id": {o.clientID}}
	return o.endSession + "?" + q.Encode(), nil
}
