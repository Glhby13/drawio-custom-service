package edge

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
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
)

const clientID = "drawio-lab"

type codeEntry struct {
	nonce, challenge, sub string
	roles                 []string
	otherKey              bool
	azp                   string
	nonceOverride         string
}

type fakeIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	other *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string]codeEntry
}

func newIdP(t *testing.T) *fakeIdP {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	o, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &fakeIdP{key: k, other: o, codes: map[string]codeEntry{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		b := p.srv.URL
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": b, "authorization_endpoint": b + "/auth", "token_endpoint": b + "/token",
			"jwks_uri": b + "/certs", "end_session_endpoint": b + "/logout", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/certs", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", p.token)
	p.srv = httptest.NewTLSServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeIdP) sign(key *rsa.PrivateKey, claims map[string]any) string {
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		panic(err)
	}
	s, err := jwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		panic(err)
	}
	return s
}

func (p *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	now := time.Now()
	key := p.key
	base := func(sub string, extra map[string]any) map[string]any {
		m := map[string]any{"iss": p.srv.URL, "sub": sub, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	if r.Form.Get("grant_type") == "password" {
		user := r.Form.Get("username")
		pass := r.Form.Get("password")
		if pass != "ad-valid-pass" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid user credentials"}`))
			return
		}
		id := p.sign(key, base("sub-"+user, map[string]any{"aud": clientID, "preferred_username": user, "name": "AD User " + user, "email": user + "@btn.co.id"}))
		at := p.sign(key, base("sub-"+user, map[string]any{"aud": "account", "azp": clientID, "resource_access": map[string]any{clientID: map[string]any{"roles": []string{"user"}}}}))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": at, "id_token": id, "token_type": "Bearer", "expires_in": 300})
		return
	}
	p.mu.Lock()
	e, ok := p.codes[r.Form.Get("code")]
	delete(p.codes, r.Form.Get("code"))
	p.mu.Unlock()
	h := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(h[:]) != e.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	if e.otherKey {
		key = p.other
	}
	nonce := e.nonce
	if e.nonceOverride != "" {
		nonce = e.nonceOverride
	}
	azp := clientID
	if e.azp != "" {
		azp = e.azp
	}
	id := p.sign(key, base(e.sub, map[string]any{"aud": clientID, "nonce": nonce, "preferred_username": "alice", "name": "Alice A", "email": "a@lab.local"}))
	at := p.sign(key, base(e.sub, map[string]any{"aud": "account", "azp": azp, "resource_access": map[string]any{clientID: map[string]any{"roles": e.roles}}}))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": at, "id_token": id, "token_type": "Bearer", "expires_in": 300})
}

type env struct {
	clock    func() time.Time
	offset   time.Duration
	t        *testing.T
	idp      *fakeIdP
	mr       *miniredis.Miniredis
	h        http.Handler
	upstream *httptest.Server
	seen     struct {
		sync.Mutex
		cookie, auth, path string
	}
	verifier *identity.Verifier
	platform *httptest.Server
	plat     struct {
		sync.Mutex
		auth, cookie, path, origin string
	}
}

func newEnv(t *testing.T, mut func(*config.Gateway)) *env {
	t.Helper()
	e := &env{t: t, idp: newIdP(t)}
	e.clock = func() time.Time { return time.Now().Add(e.offset) }
	e.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.seen.Lock()
		e.seen.cookie, e.seen.auth, e.seen.path = r.Header.Get("Cookie"), r.Header.Get("Authorization"), r.URL.Path
		e.seen.Unlock()
		w.Header().Set("Server", "Apache-Coyote")
		w.Header().Set("Set-Cookie", "JSESSIONID=abc; Path=/")
		_, _ = io.WriteString(w, "<html>draw.io</html>")
	}))
	t.Cleanup(e.upstream.Close)
	e.platform = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.plat.Lock()
		e.plat.auth, e.plat.cookie, e.plat.path, e.plat.origin = r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.URL.Path, r.Header.Get("Origin")
		e.plat.Unlock()
		if r.URL.Path == "/api/v1/internal/auth/local" { // the platform's credential check (service token only)
			c, err := e.verifier.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "platform")
			if err != nil || c.ActorType != "service" || c.Subject != "gateway-auth" {
				http.Error(w, "service token required", http.StatusForbidden)
				return
			}
			var in struct{ Username, Password string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Username == "budi" && in.Password == "correct horse battery" {
				_, _ = io.WriteString(w, `{"sub":"local:budi","username":"budi","name":"Budi","email":"b@x.test","role":"user"}`)
				return
			}
			http.Error(w, `{"error":{"code":"invalid_credentials"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Server", "platform")
		w.Header().Set("Set-Cookie", "x=y")
		_, _ = io.WriteString(w, `{"documents":[]}`)
	}))
	t.Cleanup(e.platform.Close)
	mr := miniredis.RunT(t)
	e.mr = mr
	pool := x509.NewCertPool()
	pool.AddCert(e.idp.srv.Certificate())
	up, _ := url.Parse(e.upstream.URL)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pu, _ := url.Parse("https://drawio.test")
	plu, _ := url.Parse(e.platform.URL)
	cfg := config.Gateway{PlatformUpstream: plu, Port: "0", PublicURL: pu, IssuerURL: e.idp.srv.URL, ClientID: clientID, ClientSecret: "s", RequiredRole: "user",
		DrawioUpstream: up, SessionTTL: time.Hour, SessionIdle: 10 * time.Minute, RateBurst: 1000, RateRPS: 1000, InternalKey: priv, InternalKeyID: "k"}
	if mut != nil {
		mut(&cfg)
	}
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr(), DialTimeout: time.Second, ReadTimeout: time.Second})
	t.Cleanup(func() { _ = rc.Close() })
	signer := identity.NewSigner(priv, "k", "drawio-gateway")
	e.verifier = identity.NewVerifier(pub, "k", "drawio-gateway")
	srv, err := New(cfg, slog.New(slog.NewTextHandler(testWriter{t}, nil)), session.NewRedis(rc, cfg.SessionIdle).WithClock(e.clock),
		NewOIDC(cfg.IssuerURL, cfg.ClientID, cfg.ClientSecret, cfg.PublicURL.String()+"/auth/callback", pool), signer)
	if err != nil {
		t.Fatal(err)
	}
	e.h = srv.Handler()
	return e
}

type jar map[string]string

func (j jar) absorb(res *http.Response) {
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(j, c.Name)
		} else {
			j[c.Name] = c.Value
		}
	}
}

// advance moves both the Redis TTL clock and the store's wall clock.
func (e *env) advance(d time.Duration) { e.offset += d; e.mr.FastForward(d) }

func (e *env) do(j jar, method, target string, hdr map[string]string) *http.Response {
	e.t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for k, v := range j {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	res := w.Result()
	if j != nil {
		j.absorb(res)
	}
	return res
}

var nav = map[string]string{"Sec-Fetch-Mode": "navigate", "Accept": "text/html"}

// loginWith runs the browser side of the code flow against the fake IdP and returns the callback response.
func (e *env) loginWith(j jar, entry codeEntry) *http.Response {
	e.t.Helper()
	res := e.do(j, "GET", "/login/sso?return_to=/editor/index.html", nav)
	if res.StatusCode != http.StatusFound {
		e.t.Fatalf("login status %d", res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" {
		e.t.Fatalf("authorization request lacks PKCE/state/nonce: %v", q)
	}
	entry.nonce, entry.challenge = q.Get("nonce"), q.Get("code_challenge")
	if entry.sub == "" {
		entry.sub = "user-1"
	}
	code := "code-" + session.RandomToken(6)
	e.idp.mu.Lock()
	e.idp.codes[code] = entry
	e.idp.mu.Unlock()
	return e.do(j, "GET", "/auth/callback?code="+code+"&state="+url.QueryEscape(q.Get("state")), nav)
}

func (e *env) loggedIn() jar {
	e.t.Helper()
	j := jar{}
	if res := e.loginWith(j, codeEntry{roles: []string{"user"}}); res.StatusCode != http.StatusFound || j["__Host-drawio_sid"] == "" {
		e.t.Fatalf("login failed: %d %v", res.StatusCode, j)
	}
	return j
}

func TestUnauthenticatedIsRejected(t *testing.T) {
	e := newEnv(t, nil)
	if res := e.do(nil, "GET", "/editor/index.html", nav); res.StatusCode != 302 || !strings.HasPrefix(res.Header.Get("Location"), "/login?return_to=") {
		t.Fatalf("navigation: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res := e.do(nil, "GET", "/editor/js/app.min.js", map[string]string{"Accept": "*/*"}); res.StatusCode != 401 {
		t.Fatalf("xhr: %d", res.StatusCode)
	}
	if res := e.do(nil, "GET", "/", nav); res.StatusCode != 302 {
		t.Fatalf("host page: %d", res.StatusCode)
	}
	if res := e.do(nil, "GET", "/api/me", nil); res.StatusCode != 401 {
		t.Fatalf("api/me: %d", res.StatusCode)
	}
	if res := e.do(jar{"__Host-drawio_sid": "forged"}, "GET", "/editor/", nav); res.StatusCode != 302 {
		t.Fatalf("forged cookie: %d", res.StatusCode)
	}
	if e.seen.path != "" {
		t.Fatal("an unauthenticated request reached draw.io")
	}
}

func TestLoginProxyAndLogout(t *testing.T) {
	e := newEnv(t, nil)
	j := jar{}
	res := e.loginWith(j, codeEntry{roles: []string{"user"}})
	if res.StatusCode != 302 || res.Header.Get("Location") != "/editor/index.html" {
		t.Fatalf("callback: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	var sc *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "__Host-drawio_sid" {
			sc = c
		}
	}
	if sc == nil || !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteLaxMode || sc.Path != "/" || sc.Domain != "" {
		t.Fatalf("session cookie flags: %+v", sc)
	}
	if res = e.do(j, "GET", "/editor/js/app.min.js", map[string]string{"Cookie": "evil=1", "Authorization": "Bearer x"}); res.StatusCode != 200 {
		t.Fatalf("proxy: %d", res.StatusCode)
	}
	e.seen.Lock()
	cookie, auth, path := e.seen.cookie, e.seen.auth, e.seen.path
	e.seen.Unlock()
	if cookie != "" || auth != "" || path != "/editor/js/app.min.js" {
		t.Fatalf("upstream saw cookie=%q auth=%q path=%q", cookie, auth, path)
	}
	if res.Header.Get("Set-Cookie") != "" || res.Header.Get("Server") != "" || !strings.Contains(strings.Join(res.Header.Values("Content-Security-Policy"), ";"), "frame-ancestors 'self'") || res.Header.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("proxied response headers: %v", res.Header)
	}
	res = e.do(j, "GET", "/api/me", nil)
	var me map[string]any
	_ = json.NewDecoder(res.Body).Decode(&me)
	if res.StatusCode != 200 || me["username"] != "alice" || me["csrf"] == "" {
		t.Fatalf("me: %d %v", res.StatusCode, me)
	}
	csrf, _ := me["csrf"].(string)
	origin := map[string]string{"Origin": "https://drawio.test"}
	if res = e.do(j, "POST", "/logout", nil); res.StatusCode != 403 {
		t.Fatalf("logout without origin: %d", res.StatusCode)
	}
	if res = e.do(j, "POST", "/logout", origin); res.StatusCode != 403 {
		t.Fatalf("logout without csrf token: %d", res.StatusCode)
	}
	if res = e.do(j, "POST", "/logout", map[string]string{"Origin": "https://evil.test", "X-CSRF-Token": csrf}); res.StatusCode != 403 {
		t.Fatalf("logout from foreign origin: %d", res.StatusCode)
	}
	saved := jar{"__Host-drawio_sid": j["__Host-drawio_sid"]}
	res = e.do(j, "POST", "/logout", map[string]string{"Origin": "https://drawio.test", "X-CSRF-Token": csrf})
	var out map[string]string
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 200 || !strings.HasPrefix(out["redirect"], e.idp.srv.URL+"/logout?") || !strings.Contains(out["redirect"], "id_token_hint=") {
		t.Fatalf("logout: %d %v", res.StatusCode, out)
	}
	if res = e.do(saved, "GET", "/api/me", nil); res.StatusCode != 401 {
		t.Fatalf("session survived logout: %d", res.StatusCode)
	}
}

func TestCallbackRejections(t *testing.T) {
	e := newEnv(t, nil)
	cases := map[string]codeEntry{
		"no client role":         {roles: nil},
		"unrelated role":         {roles: []string{"viewer"}},
		"id token other key":     {roles: []string{"user"}, otherKey: true},
		"nonce mismatch":         {roles: []string{"user"}, nonceOverride: "attacker"},
		"access token wrong azp": {roles: []string{"user"}, azp: "other-client"},
	}
	for name, entry := range cases {
		j := jar{}
		res := e.loginWith(j, entry)
		if res.StatusCode != 403 || j["__Host-drawio_sid"] != "" {
			t.Errorf("%s: status %d, session cookie %q", name, res.StatusCode, j["__Host-drawio_sid"])
		}
	}
	// admin role alone is accepted (coarse check allows application admins)
	if res := e.loginWith(jar{}, codeEntry{roles: []string{"admin"}}); res.StatusCode != 302 {
		t.Errorf("admin role: %d", res.StatusCode)
	}
}

func TestLoginStateIsBoundSingleUseAndShortLived(t *testing.T) {
	e := newEnv(t, nil)
	start := func(j jar) (state string) {
		res := e.do(j, "GET", "/login/sso", nav)
		loc, _ := url.Parse(res.Header.Get("Location"))
		return loc.Query().Get("state")
	}
	// replay: the second use of the same state fails
	j := jar{}
	st := start(j)
	e.idp.codes["c1"] = codeEntry{roles: []string{"user"}}
	if res := e.do(j, "GET", "/auth/callback?code=c1&state="+url.QueryEscape(st), nav); res.StatusCode == 302 {
		t.Fatal("bogus code accepted")
	}
	if res := e.do(j, "GET", "/auth/callback?code=c1&state="+url.QueryEscape(st), nav); res.StatusCode != 400 {
		t.Fatalf("replayed state: %d", res.StatusCode)
	}
	// login CSRF: state used from another browser (no binding cookie)
	st = start(jar{})
	if res := e.do(jar{}, "GET", "/auth/callback?code=c2&state="+url.QueryEscape(st), nav); res.StatusCode != 400 {
		t.Fatalf("state without binding cookie: %d", res.StatusCode)
	}
	// login CSRF with another browser's binding cookie (attacker starts a login, victim finishes it)
	attacker, victim := jar{}, jar{}
	st = start(attacker)
	start(victim)
	e.idp.codes["c4"] = codeEntry{roles: []string{"user"}}
	if res := e.do(victim, "GET", "/auth/callback?code=c4&state="+url.QueryEscape(st), nav); res.StatusCode != 400 || victim["__Host-drawio_sid"] != "" {
		t.Fatalf("state completed with a foreign binding cookie: %d", res.StatusCode)
	}
	if res := e.do(nil, "GET", "/auth/callback?code=x&state=unknown", nav); res.StatusCode != 400 {
		t.Fatalf("unknown state: %d", res.StatusCode)
	}
	if res := e.do(nil, "GET", "/auth/callback?error=access_denied&state=x", nav); res.StatusCode != 403 {
		t.Fatalf("idp error: %d", res.StatusCode)
	}
	// expiry of the login state
	j = jar{}
	st = start(j)
	e.mr.FastForward(11 * time.Minute)
	if res := e.do(j, "GET", "/auth/callback?code=c3&state="+url.QueryEscape(st), nav); res.StatusCode != 400 {
		t.Fatalf("expired login state: %d", res.StatusCode)
	}
}

func TestSessionExpiryAndStoreFailure(t *testing.T) {
	e := newEnv(t, nil)
	j := e.loggedIn()
	e.advance(11 * time.Minute) // idle timeout is 10 minutes
	if res := e.do(j, "GET", "/editor/", nav); res.StatusCode != 302 {
		t.Fatalf("idle-expired session: %d", res.StatusCode)
	}
	j = e.loggedIn()
	e.mr.Close() // session store down: fail closed
	if res := e.do(j, "GET", "/editor/", nav); res.StatusCode != 503 {
		t.Fatalf("store down: %d", res.StatusCode)
	}
	if res := e.do(nil, "GET", "/readyz", nil); res.StatusCode != 503 {
		t.Fatalf("readyz with store down: %d", res.StatusCode)
	}
	if res := e.do(nil, "GET", "/healthz", nil); res.StatusCode != 200 {
		t.Fatalf("healthz must stay up: %d", res.StatusCode)
	}
}

func TestAbsoluteSessionLimit(t *testing.T) {
	e := newEnv(t, func(c *config.Gateway) { c.SessionTTL = 2 * time.Minute; c.SessionIdle = time.Hour })
	j := e.loggedIn()
	e.advance(90 * time.Second)
	if res := e.do(j, "GET", "/api/me", nil); res.StatusCode != 200 {
		t.Fatalf("within limit: %d", res.StatusCode)
	}
	e.advance(40 * time.Second)
	if res := e.do(j, "GET", "/api/me", nil); res.StatusCode != 401 {
		t.Fatalf("past absolute limit (sliding idle must not extend it): %d", res.StatusCode)
	}
}

func TestUpstreamDownAndIdPDown(t *testing.T) {
	e := newEnv(t, nil)
	j := e.loggedIn()
	e.upstream.Close()
	if res := e.do(j, "GET", "/editor/", nav); res.StatusCode != 502 {
		t.Fatalf("upstream down: %d", res.StatusCode)
	}
	e2 := newEnv(t, nil)
	e2.idp.srv.Close()
	if res := e2.do(nil, "GET", "/login/sso", nav); res.StatusCode != 503 {
		t.Fatalf("idp down on login: %d", res.StatusCode)
	}
	if res := e2.do(nil, "GET", "/readyz", nil); res.StatusCode != 200 || res.Header.Get("X-SSO") != "unavailable" {
		t.Fatalf("readyz with the IdP down must stay ready (local accounts still work) and flag SSO: %d %q", res.StatusCode, res.Header.Get("X-SSO"))
	}
	if res := e2.do(nil, "GET", "/login", nav); res.StatusCode != 200 {
		t.Fatalf("the sign-in page must render while the IdP is down: %d", res.StatusCode)
	}
}

func TestOriginCheckCoversUpgradeAndUnsafeMethods(t *testing.T) {
	e := newEnv(t, nil)
	j := e.loggedIn()
	if res := e.do(j, "GET", "/editor/ws", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Origin": "https://evil.test"}); res.StatusCode != 403 {
		t.Fatalf("cross-origin websocket: %d", res.StatusCode)
	}
	if res := e.do(j, "POST", "/editor/x", map[string]string{"Origin": "https://evil.test"}); res.StatusCode != 403 {
		t.Fatalf("cross-origin POST: %d", res.StatusCode)
	}
	if res := e.do(j, "POST", "/editor/x", map[string]string{"Origin": "https://drawio.test"}); res.StatusCode != 200 {
		t.Fatalf("same-origin POST: %d", res.StatusCode)
	}
	if res := e.do(j, "POST", "/editor/x", map[string]string{"Referer": "https://drawio.test/page"}); res.StatusCode != 200 {
		t.Fatalf("same-origin referer fallback: %d", res.StatusCode)
	}
}

func TestHostPageAssetsAndHeaders(t *testing.T) {
	e := newEnv(t, nil)
	j := e.loggedIn()
	res := e.do(j, "GET", "/", nav)
	body, _ := io.ReadAll(res.Body)
	csp := res.Header.Get("Content-Security-Policy")
	if res.StatusCode != 200 || !strings.Contains(string(body), `/static/adapter.js`) || !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline'") && strings.Contains(csp, "script-src 'self' 'unsafe") || !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Fatalf("host page: %d csp=%q", res.StatusCode, csp)
	}
	if strings.Contains(string(body), "<script>") || strings.Contains(string(body), "onclick=") {
		t.Fatal("inline script would violate the host CSP")
	}
	for _, f := range []string{"adapter.js", "host.js", "host.css", "model.js", "bridge.js"} {
		if r := e.do(j, "GET", "/static/"+f, nil); r.StatusCode != 200 {
			t.Errorf("%s: %d", f, r.StatusCode)
		}
		if r := e.do(nil, "GET", "/static/"+f, nil); r.StatusCode != 401 {
			t.Errorf("%s without session: %d", f, r.StatusCode)
		}
	}
	for _, p := range []string{"/static/host.html", "/static/logged-out.html", "/static/logged-out.css", "/static/", "/static/a/b", "/nothing", "/static/..%2fhost.html"} {
		if r := e.do(j, "GET", p, nil); r.StatusCode != 404 && r.StatusCode != 301 && r.StatusCode != 308 {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
}

func TestFaviconIsPublicAndEmpty(t *testing.T) {
	e := newEnv(t, nil)
	if res := e.do(nil, "GET", "/favicon.ico", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("favicon: %d", res.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, func(c *config.Gateway) { c.RateBurst = 3; c.RateRPS = 0.001 })
	got429 := false
	for i := 0; i < 8; i++ {
		if res := e.do(nil, "GET", "/login/sso", nav); res.StatusCode == 429 {
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("login was not rate limited")
	}
}

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{"": "/", "/ok?x=1": "/ok?x=1", "//evil.test": "/", "https://evil.test": "/", "/\\evil.test": "/", "evil": "/", "/a\r\nb": "/", "/%0d": "/%0d"} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q)=%q want %q", in, got, want)
		}
	}
}

func TestInternalTokenIsNotExposedToDrawio(t *testing.T) {
	e := newEnv(t, nil)
	j := e.loggedIn()
	e.do(j, "GET", "/editor/", nav)
	e.seen.Lock()
	defer e.seen.Unlock()
	if e.seen.auth != "" || e.seen.cookie != "" {
		t.Fatal("identity material reached draw.io")
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// The signed-out page is public (the session is gone), self-contained, and its CSP allows exactly its own stylesheet.
func TestLoggedOutPageIsPublicSelfContainedAndHashPinned(t *testing.T) {
	e := newEnv(t, nil)
	res := e.do(nil, "GET", "/logged-out", nil)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("logged-out: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	html := string(body)
	for _, want := range []string{"Sign in again", `href="/login"`, "signed out"} {
		if !strings.Contains(html, want) {
			t.Errorf("logged-out page misses %q", want)
		}
	}
	// the inline stylesheet must be exactly what the CSP hash covers
	start, end := strings.Index(html, "<style>"), strings.Index(html, "</style>")
	if start < 0 || end < start {
		t.Fatal("no inline stylesheet")
	}
	sum := sha256.Sum256([]byte(html[start+len("<style>") : end]))
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
		t.Fatalf("CSP does not pin the inline stylesheet: %q", csp)
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "script-src", "http://", "https://"} {
		if strings.Contains(csp, bad) {
			t.Errorf("CSP contains %q: %q", bad, csp)
		}
	}
	if strings.Contains(html, "<script") || strings.Contains(html, "onclick=") || strings.Contains(html, " style=") || strings.Contains(html, "http://") || strings.Contains(html, "https://") {
		t.Fatal("logged-out page must have no script, inline style attribute, or external reference")
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") || res.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("logged-out page must not be frameable: %q %q", csp, res.Header.Get("X-Frame-Options"))
	}
}

// The host page markup keeps the ids the e2e and the script rely on and stays inline-free (CSP style-src 'self').
func TestHostPageKeepsContractIDsAndHasNoInlineStyle(t *testing.T) {
	e := newEnv(t, nil)
	res := e.do(e.loggedIn(), "GET", "/", nav)
	body, _ := io.ReadAll(res.Body)
	html := string(body)
	for _, id := range []string{`id="status"`, `id="logout"`, `id="who"`, `id="editor"`, `data-state="loading"`} {
		if !strings.Contains(html, id) {
			t.Errorf("host page misses %s", id)
		}
	}
	if strings.Contains(html, " style=") || strings.Contains(html, "<style") {
		t.Fatal("host page must not use inline styles (CSP style-src 'self')")
	}
}

func TestPlatformProxyMintsAnInternalTokenAndDropsBrowserCredentials(t *testing.T) {
	e := newEnv(t, nil)
	if res := e.do(nil, "GET", "/api/v1/documents", nil); res.StatusCode != 401 {
		t.Fatalf("unauthenticated API call must be 401, got %d", res.StatusCode)
	}
	j := e.loggedIn()
	res := e.do(j, "GET", "/api/v1/documents", map[string]string{"Authorization": "Bearer forged.client.token", "Origin": "https://drawio.test"})
	if res.StatusCode != 200 {
		t.Fatalf("API call: %d", res.StatusCode)
	}
	e.plat.Lock()
	auth, cookie, path := e.plat.auth, e.plat.cookie, e.plat.path
	e.plat.Unlock()
	if path != "/api/v1/documents" || cookie != "" {
		t.Fatalf("upstream saw path %q cookie %q (cookies must not cross)", path, cookie)
	}
	tok := strings.TrimPrefix(auth, "Bearer ")
	if tok == "" || tok == "forged.client.token" {
		t.Fatalf("client Authorization leaked or no token minted: %q", auth)
	}
	c, err := e.verifier.Verify(tok, "platform")
	if err != nil || c.ActorType != "human" || c.Username == "" || len(c.Roles) == 0 || c.ExpiresAt-c.IssuedAt > 60 {
		t.Fatalf("minted token wrong: %+v %v", c, err)
	}
	if _, err := e.verifier.Verify(tok, "other-service"); err == nil {
		t.Fatal("token must be bound to the platform audience")
	}
	if res.Header.Get("Set-Cookie") != "" || res.Header.Get("Server") != "" {
		t.Fatalf("upstream cookie/banner leaked: %v", res.Header)
	}
	// cross-site state-changing calls and WebSocket upgrades are refused at the edge
	if r := e.do(j, "POST", "/api/v1/documents", map[string]string{"Origin": "https://evil.test"}); r.StatusCode != 403 {
		t.Fatalf("cross-origin POST: %d", r.StatusCode)
	}
	if r := e.do(j, "GET", "/ws/v1/documents/x", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Origin": "https://evil.test"}); r.StatusCode != 403 {
		t.Fatalf("cross-origin websocket: %d", r.StatusCode)
	}
	// the SPA route serves the host page for navigations and still requires a session
	if r := e.do(nil, "GET", "/d/abc", nav); r.StatusCode != 302 {
		t.Fatalf("/d/{id} without session: %d", r.StatusCode)
	}
	if r := e.do(j, "GET", "/d/abc", nav); r.StatusCode != 200 {
		t.Fatalf("/d/{id} with session: %d", r.StatusCode)
	}
}

func (e *env) postJSON(j jar, path string, body string, hdr map[string]string) *http.Response {
	e.t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://drawio.test")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for k, v := range j {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	res := w.Result()
	if j != nil {
		j.absorb(res)
	}
	return res
}

func TestLoginPageAndLocalSignIn(t *testing.T) {
	e := newEnv(t, nil)
	res := e.do(nil, "GET", "/login", nav)
	body, _ := io.ReadAll(res.Body)
	csp := res.Header.Get("Content-Security-Policy")
	if res.StatusCode != 200 || !strings.Contains(string(body), "DrawSpace") || !strings.Contains(string(body), `href="/login/sso"`) || strings.Contains(csp, "unsafe-inline") ||
		!strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") || res.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("login page: %d csp=%q", res.StatusCode, csp)
	}
	if r := e.do(nil, "GET", "/login-assets/login.js", nil); r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("login.js: %d", r.StatusCode)
	}
	if r := e.do(nil, "GET", "/favicon.svg", nil); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("favicon: %d", r.StatusCode)
	}
	if r := e.do(nil, "GET", "/login-assets/logo-light.png", nil); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("logo-light: %d", r.StatusCode)
	}
	if r := e.do(nil, "GET", "/login-assets/logo-dark.png", nil); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("logo-dark: %d", r.StatusCode)
	}
	// wrong password: generic 401, no cookie
	j := jar{}
	if r := e.postJSON(j, "/login/local", `{"username":"budi","password":"nope nope nope"}`, nil); r.StatusCode != 401 || len(j) != 0 {
		t.Fatalf("bad password: %d %v", r.StatusCode, j)
	}
	// cross-site sign-in attempts are refused by the origin rule
	if r := e.postJSON(nil, "/login/local", `{"username":"budi","password":"correct horse battery"}`, map[string]string{"Origin": "https://evil.test"}); r.StatusCode != 403 {
		t.Fatalf("cross-origin sign-in: %d", r.StatusCode)
	}
	// success: session cookie, redirect honours a same-site return_to only
	r := e.postJSON(j, "/login/local", `{"username":" Budi ","password":"correct horse battery","return_to":"//evil.test/x"}`, nil)
	var out map[string]string
	_ = json.NewDecoder(r.Body).Decode(&out)
	if r.StatusCode != 200 || out["redirect"] != "/" || j["__Host-drawio_sid"] == "" {
		t.Fatalf("sign-in: %d %v %v", r.StatusCode, out, j)
	}
	me := e.do(j, "GET", "/api/me", nil)
	var m map[string]any
	_ = json.NewDecoder(me.Body).Decode(&m)
	if m["username"] != "budi" || m["idp"] != "local" || m["sub"] != "local:budi" {
		t.Fatalf("me: %v", m)
	}
	// the platform sees a gateway-minted token that says "local"
	if r := e.do(j, "GET", "/api/v1/documents", nil); r.StatusCode != 200 {
		t.Fatalf("api: %d", r.StatusCode)
	}
	e.plat.Lock()
	auth := e.plat.auth
	e.plat.Unlock()
	c, err := e.verifier.Verify(strings.TrimPrefix(auth, "Bearer "), "platform")
	if err != nil || c.Idp != "local" || c.Subject != "local:budi" || c.ActorType != "human" {
		t.Fatalf("token: %+v %v", c, err)
	}
	// browsers can never reach the platform's service endpoint
	if r := e.do(j, "GET", "/api/v1/internal/auth/local", nil); r.StatusCode != 404 {
		t.Fatalf("internal endpoint reachable from a browser: %d", r.StatusCode)
	}
	if r := e.postJSON(j, "/api/v1/internal/auth/local", `{"username":"budi","password":"correct horse battery"}`, nil); r.StatusCode != 404 {
		t.Fatalf("internal endpoint reachable from a browser (POST): %d", r.StatusCode)
	}
	// already signed in: the login page sends you on
	if r := e.do(j, "GET", "/login?return_to=/d/abc", nav); r.StatusCode != 302 || r.Header.Get("Location") != "/d/abc" {
		t.Fatalf("login page while signed in: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	// logout of a local session does not involve the IdP
	lo := e.postJSON(j, "/logout", `{}`, map[string]string{"X-CSRF-Token": m["csrf"].(string)})
	var lr map[string]string
	_ = json.NewDecoder(lo.Body).Decode(&lr)
	if lo.StatusCode != 200 || lr["redirect"] != "/logged-out" {
		t.Fatalf("local logout: %d %v", lo.StatusCode, lr)
	}
}

func TestLocalSignInLocksAfterRepeatedFailures(t *testing.T) {
	e := newEnv(t, nil)
	for i := 0; i < maxLocalFailures; i++ {
		if r := e.postJSON(nil, "/login/local", `{"username":"budi","password":"wrong password"}`, nil); r.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	// now even the right password is refused until the window passes
	if r := e.postJSON(nil, "/login/local", `{"username":"budi","password":"correct horse battery"}`, nil); r.StatusCode != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("locked account: %d", r.StatusCode)
	}
	e.advance(failWindow + time.Minute)
	if r := e.postJSON(nil, "/login/local", `{"username":"budi","password":"correct horse battery"}`, nil); r.StatusCode != 200 {
		t.Fatalf("after the window: %d", r.StatusCode)
	}
}

func TestLocalSignInWorksWhileTheIdentityProviderIsDown(t *testing.T) {
	e := newEnv(t, nil)
	e.idp.srv.Close()
	if r := e.postJSON(nil, "/login/local", `{"username":"budi","password":"correct horse battery"}`, nil); r.StatusCode != 200 {
		t.Fatalf("local sign-in with SSO down: %d", r.StatusCode)
	}
}

func TestADSignIn(t *testing.T) {
	e := newEnv(t, nil)
	j := jar{}
	// wrong password: 401
	if r := e.postJSON(j, "/login/ad", `{"username":"john","password":"wrong"}`, nil); r.StatusCode != 401 || len(j) != 0 {
		t.Fatalf("bad password: %d %v", r.StatusCode, j)
	}
	// success: sets cookie, idp=sso
	r := e.postJSON(j, "/login/ad", `{"username":"john","password":"ad-valid-pass","return_to":"/d/doc1"}`, nil)
	var out map[string]string
	_ = json.NewDecoder(r.Body).Decode(&out)
	if r.StatusCode != 200 || out["redirect"] != "/d/doc1" || j["__Host-drawio_sid"] == "" {
		t.Fatalf("ad sign-in: %d %v %v", r.StatusCode, out, j)
	}
	me := e.do(j, "GET", "/api/me", nil)
	var m map[string]any
	_ = json.NewDecoder(me.Body).Decode(&m)
	if m["username"] != "john" || m["idp"] != "sso" || m["sub"] != "sub-john" {
		t.Fatalf("me: %v", m)
	}
}

func TestADSignInLocksAfterFailures(t *testing.T) {
	e := newEnv(t, nil)
	for i := 0; i < maxLocalFailures; i++ {
		if r := e.postJSON(nil, "/login/ad", `{"username":"john","password":"wrong"}`, nil); r.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	if r := e.postJSON(nil, "/login/ad", `{"username":"john","password":"ad-valid-pass"}`, nil); r.StatusCode != 429 {
		t.Fatalf("locked account: %d", r.StatusCode)
	}
	e.advance(failWindow + time.Minute)
	if r := e.postJSON(nil, "/login/ad", `{"username":"john","password":"ad-valid-pass"}`, nil); r.StatusCode != 200 {
		t.Fatalf("after window: %d", r.StatusCode)
	}
}

func TestLoginLogoSVGAssets(t *testing.T) {
	e := newEnv(t, nil)
	for _, p := range []string{
		"/login-assets/drawspace-logo.svg",
		"/login-assets/drawspace-logo-dark.svg",
		"/login-assets/drawspace-icon.svg",
		"/login-assets/drawspace-wordmark.svg",
		"/favicon.svg",
	} {
		r := e.do(nil, "GET", p, nil)
		if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "image/svg+xml") {
			t.Fatalf("%s: status %d ct %q", p, r.StatusCode, r.Header.Get("Content-Type"))
		}
	}
}
