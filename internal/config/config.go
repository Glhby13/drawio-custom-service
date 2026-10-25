// Package config loads gateway settings from the environment. Secrets (client secret, signing key,
// Redis password) come from Kubernetes Secrets mounted as env vars and are never logged.
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Gateway holds every setting of the gateway deployable.
type Gateway struct {
	Port             string
	PublicURL        *url.URL // https://drawio.lab.local (browser-facing origin)
	IssuerURL        string
	ClientID         string
	ClientSecret     string
	RequiredRole     string // client role needed to enter at all (coarse check, ADR-0004)
	DrawioUpstream   *url.URL
	PlatformUpstream *url.URL // optional: when empty /api/v1 and /ws/v1 are not served
	RedisAddr        string
	RedisUser        string
	RedisPassword    string
	CAFile           string // extra PEM bundle trusted for the issuer (lab wildcard CA)
	SessionTTL       time.Duration
	SessionIdle      time.Duration
	RateBurst        int
	RateRPS          float64
	InternalKey      ed25519.PrivateKey
	InternalKeyID    string
	InsecureCookie   bool // plain-HTTP local tests only; refused when PUBLIC_URL is https
}

// LoadGateway reads and validates the environment; getenv is injectable for tests.
func LoadGateway(getenv func(string) string) (Gateway, error) {
	g := Gateway{
		Port:          or(getenv("PORT"), "8080"),
		IssuerURL:     strings.TrimRight(getenv("ISSUER_URL"), "/"),
		ClientID:      getenv("OIDC_CLIENT_ID"),
		ClientSecret:  getenv("OIDC_CLIENT_SECRET"),
		RequiredRole:  or(getenv("REQUIRED_ROLE"), "user"),
		RedisAddr:     getenv("REDIS_ADDR"),
		RedisUser:     getenv("REDIS_USER"),
		RedisPassword: getenv("REDIS_PASSWORD"),
		CAFile:        getenv("EXTRA_CA_FILE"),
		InternalKeyID: or(getenv("INTERNAL_KEY_ID"), "edge-1"),
	}
	var errs []error
	pu, err := parseURL("PUBLIC_URL", getenv("PUBLIC_URL"))
	errs = append(errs, err)
	g.PublicURL = pu
	du, err := parseURL("DRAWIO_UPSTREAM", getenv("DRAWIO_UPSTREAM"))
	errs = append(errs, err)
	g.DrawioUpstream = du
	for name, v := range map[string]string{"ISSUER_URL": g.IssuerURL, "OIDC_CLIENT_ID": g.ClientID, "OIDC_CLIENT_SECRET": g.ClientSecret, "REDIS_ADDR": g.RedisAddr} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	if !strings.HasPrefix(g.IssuerURL, "https://") {
		errs = append(errs, errors.New("ISSUER_URL must be https"))
	}
	if v := getenv("PLATFORM_UPSTREAM"); v != "" {
		pu2, err := parseURL("PLATFORM_UPSTREAM", v)
		errs = append(errs, err)
		g.PlatformUpstream = pu2
	}
	g.InsecureCookie = getenv("INSECURE_COOKIE") == "true"
	if pu != nil && pu.Scheme != "https" && !g.InsecureCookie {
		errs = append(errs, errors.New("PUBLIC_URL must be https (set INSECURE_COOKIE=true for local tests only)"))
	}
	if pu != nil && pu.Scheme == "https" && g.InsecureCookie {
		errs = append(errs, errors.New("INSECURE_COOKIE is refused with an https PUBLIC_URL"))
	}
	g.SessionTTL, err = dur(getenv, "SESSION_TTL", 8*time.Hour)
	errs = append(errs, err)
	g.SessionIdle, err = dur(getenv, "SESSION_IDLE", 30*time.Minute)
	errs = append(errs, err)
	g.RateBurst = 30
	g.RateRPS = 10
	if v := getenv("RATE_BURST"); v != "" {
		n, e := strconv.Atoi(v)
		errs = append(errs, e)
		g.RateBurst = n
	}
	seed, err := base64.StdEncoding.DecodeString(getenv("INTERNAL_KEY_SEED"))
	if err != nil || len(seed) != ed25519.SeedSize {
		errs = append(errs, errors.New("INTERNAL_KEY_SEED must be base64 of 32 bytes"))
	} else {
		g.InternalKey = ed25519.NewKeyFromSeed(seed)
	}
	if e := errors.Join(errs...); e != nil {
		return Gateway{}, e
	}
	return g, nil
}

func or(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func parseURL(name, raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%s is not a valid http(s) URL", name)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func dur(getenv func(string) string, name string, d time.Duration) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return d, nil
	}
	p, err := time.ParseDuration(v)
	if err != nil || p <= 0 {
		return 0, fmt.Errorf("%s is not a positive duration", name)
	}
	return p, nil
}
