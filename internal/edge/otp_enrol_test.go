package edge

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base32"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/redis/go-redis/v9"

	"github.com/Glhby13/drawio-custom-service/internal/config"
	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/session"
	"github.com/Glhby13/drawio-custom-service/internal/totp"
)

// kcUser is one realm user; otpRaw is the raw secret of its OTP credential ("" = no OTP credential).
type kcUser struct {
	id, username, password string
	roles                  []string
	otpRaw                 string
}

// fakeKeycloak behaves like the realm for client drawio-lab: the direct-grant flow asks for the code only when the
// user holds an OTP credential AND the drawio-admin role; the Admin API lists users and credentials and registers OTP.
type fakeKeycloak struct {
	srv        *httptest.Server
	key        *rsa.PrivateKey
	mu         sync.Mutex
	users      map[string]*kcUser // by username
	denyWrites bool
}

func hasRole(roles []string, r string) bool {
	for _, x := range roles {
		if x == r {
			return true
		}
	}
	return false
}

func newFakeKeycloak(t *testing.T, users ...*kcUser) *fakeKeycloak {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeKeycloak{key: k, users: map[string]*kcUser{}}
	for _, u := range users {
		f.users[u.username] = u
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/lab/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		b := f.srv.URL + "/realms/lab"
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": b, "authorization_endpoint": b + "/auth", "token_endpoint": b + "/token",
			"jwks_uri": b + "/certs", "end_session_endpoint": b + "/logout", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/realms/lab/certs", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("POST /realms/lab/token", f.token)
	mux.HandleFunc("GET /admin/realms/lab/users", f.listUsers)
	mux.HandleFunc("GET /admin/realms/lab/users/{id}", f.getUser)
	mux.HandleFunc("PUT /admin/realms/lab/users/{id}", f.putUser)
	mux.HandleFunc("GET /admin/realms/lab/users/{id}/credentials", f.credentials)
	mux.HandleFunc("GET /admin/realms/lab/users/{id}/role-mappings/clients/{cid}/composite", f.roles)
	mux.HandleFunc("GET /admin/realms/lab/clients", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"cli-1"}]`)
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKeycloak) sign(claims map[string]any) string {
	sig, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	s, _ := jwt.Signed(sig).Claims(claims).Serialize()
	return s
}

func (f *fakeKeycloak) byID(id string) *kcUser {
	for _, u := range f.users {
		if u.id == id {
			return u
		}
	}
	return nil
}

func (f *fakeKeycloak) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	w.Header().Set("Content-Type", "application/json")
	if r.Form.Get("grant_type") == "client_credentials" {
		_, _ = io.WriteString(w, `{"access_token":"svc","expires_in":300}`)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	bad := func() {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Invalid user credentials"}`)
	}
	u := f.users[r.Form.Get("username")]
	if u == nil || u.password != r.Form.Get("password") {
		bad()
		return
	}
	if u.otpRaw != "" && hasRole(u.roles, "drawio-admin") {
		b32 := strings.TrimRight(base32.StdEncoding.EncodeToString([]byte(u.otpRaw)), "=")
		if !totp.Validate(b32, r.Form.Get("totp")) {
			bad()
			return
		}
	}
	now := time.Now()
	base := map[string]any{"iss": f.srv.URL + "/realms/lab", "sub": u.id, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
	merge := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	id := f.sign(merge(map[string]any{"aud": clientID, "preferred_username": u.username, "name": u.username, "email": u.username + "@lab.local"}))
	at := f.sign(merge(map[string]any{"aud": "account", "azp": clientID,
		"resource_access": map[string]any{clientID: map[string]any{"roles": u.roles}}}))
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": at, "id_token": id, "token_type": "Bearer", "expires_in": 300})
}

func (f *fakeKeycloak) listUsers(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []map[string]any{}
	if u := f.users[r.URL.Query().Get("username")]; u != nil {
		out = append(out, map[string]any{"id": u.id, "totp": u.otpRaw != ""})
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeKeycloak) getUser(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.byID(r.PathValue("id"))
	_ = json.NewEncoder(w).Encode(map[string]any{"id": u.id, "username": u.username, "enabled": true})
}

func (f *fakeKeycloak) putUser(w http.ResponseWriter, r *http.Request) {
	var rep struct {
		Credentials []struct {
			Type       string `json:"type"`
			SecretData string `json:"secretData"`
		} `json:"credentials"`
	}
	_ = json.NewDecoder(r.Body).Decode(&rep)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.denyWrites {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	for _, c := range rep.Credentials {
		if c.Type == "otp" {
			var sd struct {
				Value string `json:"value"`
			}
			_ = json.Unmarshal([]byte(c.SecretData), &sd)
			f.byID(r.PathValue("id")).otpRaw = sd.Value
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeKeycloak) credentials(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []map[string]string{{"type": "password"}}
	if f.byID(r.PathValue("id")).otpRaw != "" {
		out = append(out, map[string]string{"type": "otp"})
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeKeycloak) roles(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []map[string]string{}
	for _, n := range f.byID(r.PathValue("id")).roles {
		out = append(out, map[string]string{"name": n})
	}
	_ = json.NewEncoder(w).Encode(out)
}

// newOTPEnv runs the real gateway against the fake Keycloak (no platform needed: AD sign-in never calls it).
func newOTPEnv(t *testing.T, users ...*kcUser) (*env, *fakeKeycloak, *OIDC) {
	t.Helper()
	kc := newFakeKeycloak(t, users...)
	mr := miniredis.RunT(t)
	pool := x509.NewCertPool()
	pool.AddCert(kc.srv.Certificate())
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pu, _ := url.Parse("https://drawio.test")
	up, _ := url.Parse("http://127.0.0.1:1")
	issuer := kc.srv.URL + "/realms/lab"
	cfg := config.Gateway{Port: "0", PublicURL: pu, IssuerURL: issuer, ClientID: clientID, ClientSecret: "s", RequiredRole: "user",
		DrawioUpstream: up, SessionTTL: time.Hour, SessionIdle: 10 * time.Minute, RateBurst: 1000, RateRPS: 1000, InternalKey: priv, InternalKeyID: "k"}
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr(), DialTimeout: time.Second, ReadTimeout: time.Second})
	t.Cleanup(func() { _ = rc.Close() })
	o := NewOIDC(issuer, clientID, "s", pu.String()+"/auth/callback", pool)
	srv, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), session.NewRedis(rc, cfg.SessionIdle), o, identity.NewSigner(priv, "k", "drawio-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, h: srv.Handler()}, kc, o
}

func decode(t *testing.T, r *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	return m
}

func sari() *kcUser {
	return &kcUser{id: "kc-sari", username: "sari", password: "pw-sari", roles: []string{"drawio-access", "drawio-admin"}}
}

func b32Of(raw string) string {
	return strings.TrimRight(base32.StdEncoding.EncodeToString([]byte(raw)), "=")
}

// The case that motivated the change: an administrator with no OTP credential is offered a QR code; the secret ends
// up in Keycloak as a native OTP credential, and Keycloak (not the gateway) verifies every later sign-in.
func TestAdminWithoutOTPEnrolsInKeycloak(t *testing.T) {
	e, kc, _ := newOTPEnv(t, sari())
	creds := `{"username":"sari","password":"pw-sari","return_to":"/d/doc1"}`

	j := jar{}
	r := e.postJSON(j, "/login/ad", creds, nil)
	body := decode(t, r)
	if r.StatusCode != 401 || body["error"] != "totp_setup_required" || len(j) != 0 {
		t.Fatalf("first sign-in must offer enrolment and create no session: %d %v %v", r.StatusCode, body, j)
	}
	secret, _ := body["secret"].(string)
	token, _ := body["setup_token"].(string)
	if secret == "" || token == "" || !strings.HasPrefix(body["qr_code"].(string), "data:image/png;base64,") {
		t.Fatalf("enrolment payload incomplete: %v", body)
	}
	if kc.users["sari"].otpRaw != "" {
		t.Fatal("the secret must not reach Keycloak before the user proves the code")
	}

	// a wrong code does not enrol and does not burn the token
	r = e.postJSON(j, "/login/ad/totp-setup", `{"setup_token":"`+token+`","code":"000000"}`, nil)
	if r.StatusCode != 401 || decode(t, r)["error"] != "invalid_code" || kc.users["sari"].otpRaw != "" {
		t.Fatalf("a wrong code must not enrol: %d", r.StatusCode)
	}

	code, err := totp.GenerateCode(secret)
	if err != nil {
		t.Fatal(err)
	}
	r = e.postJSON(j, "/login/ad/totp-setup", `{"setup_token":"`+token+`","code":"`+code+`"}`, nil)
	out := decode(t, r)
	if r.StatusCode != 200 || out["redirect"] != "/d/doc1" || j["__Host-drawio_sid"] == "" {
		t.Fatalf("right code must finish the sign-in: %d %v %v", r.StatusCode, out, j)
	}
	if got := kc.users["sari"].otpRaw; got == "" || b32Of(got) != secret {
		t.Fatalf("Keycloak must hold the same secret the QR code carried: %q", got)
	}

	// single use
	if r = e.postJSON(nil, "/login/ad/totp-setup", `{"setup_token":"`+token+`","code":"`+code+`"}`, nil); r.StatusCode != 400 {
		t.Fatalf("setup token must not work twice: %d", r.StatusCode)
	}

	// from now on Keycloak itself asks for the code (this also proves the cached "no OTP yet" answer is dropped)
	r = e.postJSON(nil, "/login/ad", creds, nil)
	if r.StatusCode != 401 || decode(t, r)["error"] != "totp_required" {
		t.Fatalf("enrolled administrator must be asked for the code: %d", r.StatusCode)
	}
	r = e.postJSON(nil, "/login/ad", `{"username":"sari","password":"pw-sari","totp":"000000"}`, nil)
	if r.StatusCode != 401 || decode(t, r)["error"] != "invalid_credentials" {
		t.Fatalf("wrong code must be refused: %d", r.StatusCode)
	}
	code, _ = totp.GenerateCode(secret)
	j2 := jar{}
	r = e.postJSON(j2, "/login/ad", `{"username":"sari","password":"pw-sari","totp":"`+code+`"}`, nil)
	if r.StatusCode != 200 || j2["__Host-drawio_sid"] == "" {
		t.Fatalf("right code must sign in: %d", r.StatusCode)
	}
}

func TestRegularUserIsNeverOfferedEnrolment(t *testing.T) {
	e, kc, _ := newOTPEnv(t, &kcUser{id: "kc-budi", username: "budi", password: "pw-budi", roles: []string{"drawio-access"}})
	j := jar{}
	r := e.postJSON(j, "/login/ad", `{"username":"budi","password":"pw-budi"}`, nil)
	if r.StatusCode != 200 || j["__Host-drawio_sid"] == "" || kc.users["budi"].otpRaw != "" {
		t.Fatalf("regular user must sign in directly: %d", r.StatusCode)
	}
}

func TestEnrolmentNeverReplacesAnExistingOTP(t *testing.T) {
	u := sari()
	u.otpRaw = "ExistingRawSecret1234"
	e, kc, o := newOTPEnv(t, u)
	if err := o.RegisterOTP(t.Context(), "kc-sari", "AttackerRawSecret1234"); !errors.Is(err, ErrOTPExists) {
		t.Fatalf("expected ErrOTPExists, got %v", err)
	}
	if kc.users["sari"].otpRaw != "ExistingRawSecret1234" {
		t.Fatal("an existing authenticator was overwritten")
	}
	// and an administrator who already has one is asked for the code, never shown a QR code
	r := e.postJSON(nil, "/login/ad", `{"username":"sari","password":"pw-sari"}`, nil)
	if r.StatusCode != 401 || decode(t, r)["error"] != "totp_required" {
		t.Fatalf("got %d", r.StatusCode)
	}
}

func TestSetupTokenDiesAfterTooManyWrongCodes(t *testing.T) {
	e, kc, _ := newOTPEnv(t, sari())
	token := decode(t, e.postJSON(nil, "/login/ad", `{"username":"sari","password":"pw-sari"}`, nil))["setup_token"].(string)
	for i := 0; i < maxSetupTries; i++ {
		if r := e.postJSON(nil, "/login/ad/totp-setup", `{"setup_token":"`+token+`","code":"000000"}`, nil); r.StatusCode != 401 {
			t.Fatalf("try %d: %d", i, r.StatusCode)
		}
	}
	r := e.postJSON(nil, "/login/ad/totp-setup", `{"setup_token":"`+token+`","code":"000000"}`, nil)
	if r.StatusCode != 400 || decode(t, r)["error"] != "invalid_or_expired_token" || kc.users["sari"].otpRaw != "" {
		t.Fatalf("token must be gone after %d wrong codes: %d", maxSetupTries, r.StatusCode)
	}
}

func TestKeycloakRefusingTheWriteLeavesNoSession(t *testing.T) {
	e, kc, _ := newOTPEnv(t, sari())
	body := decode(t, e.postJSON(nil, "/login/ad", `{"username":"sari","password":"pw-sari"}`, nil))
	kc.mu.Lock()
	kc.denyWrites = true // the service account lacks manage-users
	kc.mu.Unlock()
	code, _ := totp.GenerateCode(body["secret"].(string))
	j := jar{}
	r := e.postJSON(j, "/login/ad/totp-setup", `{"setup_token":"`+body["setup_token"].(string)+`","code":"`+code+`"}`, nil)
	if r.StatusCode != 503 || len(j) != 0 || kc.users["sari"].otpRaw != "" {
		t.Fatalf("a refused write must fail closed: %d %v", r.StatusCode, j)
	}
}
