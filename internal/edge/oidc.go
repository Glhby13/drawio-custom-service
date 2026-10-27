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

	adminCacheMu sync.RWMutex
	adminCache   map[string]adminCacheEntry
	saTokenMu    sync.Mutex
	saToken      string
	saExpiry     time.Time
	clientUUIDMu sync.RWMutex
	clientUUID   string
}

type adminCacheEntry struct {
	isAdmin   bool
	expiresAt time.Time
}

// NewOIDC builds the client; pool adds the lab CA to the system roots.
func NewOIDC(issuer, clientID, secret, redirect string, pool *x509.CertPool) *OIDC {
	tr := &http.Transport{TLSClientConfig: tlsConfig(pool), Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	return &OIDC{
		issuer:     issuer,
		clientID:   clientID,
		secret:     secret,
		redirect:   redirect,
		client:     &http.Client{Transport: tr, Timeout: 15 * time.Second},
		adminCache: make(map[string]adminCacheEntry),
	}
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

// ErrAccessDenied is returned when Keycloak refuses access (missing client role / group).
var ErrAccessDenied = errors.New("access denied")

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

		bodyLower := strings.ToLower(string(body))
		if resp.StatusCode == http.StatusForbidden ||
			errResp.Error == "access_denied" ||
			strings.Contains(bodyLower, "access denied") ||
			(resp.StatusCode == http.StatusUnauthorized && errResp.Error != "invalid_grant") {
			return nil, ErrAccessDenied
		}
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

// IsAdminUser checks whether a username has the client role "admin" (or "drawio-admin") on this client in Keycloak.
// Results are cached in-memory for 5 minutes.
func (o *OIDC) IsAdminUser(ctx context.Context, username string) bool {
	user := strings.ToLower(strings.TrimSpace(username))
	if user == "" {
		return false
	}
	o.adminCacheMu.RLock()
	if entry, ok := o.adminCache[user]; ok && time.Now().Before(entry.expiresAt) {
		o.adminCacheMu.RUnlock()
		return entry.isAdmin
	}
	o.adminCacheMu.RUnlock()

	isAdmin := o.checkAdminRemote(ctx, user)
	o.adminCacheMu.Lock()
	if o.adminCache == nil {
		o.adminCache = make(map[string]adminCacheEntry)
	}
	o.adminCache[user] = adminCacheEntry{isAdmin: isAdmin, expiresAt: time.Now().Add(5 * time.Minute)}
	o.adminCacheMu.Unlock()
	return isAdmin
}

func (o *OIDC) getServiceAccountToken(ctx context.Context) (string, error) {
	o.saTokenMu.Lock()
	defer o.saTokenMu.Unlock()
	if o.saToken != "" && time.Now().Add(30*time.Second).Before(o.saExpiry) {
		return o.saToken, nil
	}
	if err := o.ensure(); err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {o.clientID},
		"client_secret": {o.secret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.prov.Endpoint().TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sa token http %d", resp.StatusCode)
	}
	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	o.saToken = res.AccessToken
	o.saExpiry = time.Now().Add(time.Duration(res.ExpiresIn) * time.Second)
	return o.saToken, nil
}

func (o *OIDC) getClientUUID(ctx context.Context, adminBase, token string) (string, error) {
	o.clientUUIDMu.RLock()
	if o.clientUUID != "" {
		defer o.clientUUIDMu.RUnlock()
		return o.clientUUID, nil
	}
	o.clientUUIDMu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminBase+"/clients?clientId="+url.QueryEscape(o.clientID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get client http %d", resp.StatusCode)
	}
	var clients []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&clients); err != nil || len(clients) == 0 {
		return "", errors.New("client not found")
	}
	o.clientUUIDMu.Lock()
	o.clientUUID = clients[0].ID
	o.clientUUIDMu.Unlock()
	return clients[0].ID, nil
}

func (o *OIDC) checkAdminRemote(ctx context.Context, username string) bool {
	saToken, err := o.getServiceAccountToken(ctx)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.TrimRight(o.issuer, "/"), "/realms/")
	if len(parts) != 2 {
		return false
	}
	adminBase := parts[0] + "/admin/realms/" + parts[1]

	// 1. Get user id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminBase+"/users?username="+url.QueryEscape(username)+"&exact=true", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := o.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var users []struct {
		ID   string `json:"id"`
		TOTP bool   `json:"totp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil || len(users) == 0 {
		return false
	}
	userID := users[0].ID

	// 2. Get client UUID
	clientUUID, err := o.getClientUUID(ctx, adminBase, saToken)
	if err != nil {
		return false
	}

	// 3. Get composite roles on this client
	rolesReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/users/%s/role-mappings/clients/%s/composite", adminBase, userID, clientUUID), nil)
	if err != nil {
		return false
	}
	rolesReq.Header.Set("Authorization", "Bearer "+saToken)
	rolesResp, err := o.client.Do(rolesReq)
	if err != nil {
		return false
	}
	defer rolesResp.Body.Close()
	if rolesResp.StatusCode != http.StatusOK {
		return false
	}
	var roles []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(rolesResp.Body).Decode(&roles); err != nil {
		return false
	}
	for _, r := range roles {
		if r.Name == "admin" || r.Name == "drawio-admin" {
			return true
		}
	}
	return false
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
