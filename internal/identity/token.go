// Package identity mints and verifies the short-lived internal token the edge hands to internal
// services (ADR-0004). Plain identity headers are never trusted; only this signed token is.
// EdDSA (Ed25519) over a compact JWS, stdlib only.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxTTL is the hard ceiling for any internal token (ADR-0004: <= 5 minutes).
const MaxTTL = 5 * time.Minute

// Actor types (ADR-0009/0010).
const (
	ActorHuman   = "human"
	ActorAI      = "ai"
	ActorService = "service"
)

// Claims is the internal token payload.
type Claims struct {
	Issuer     string   `json:"iss"`
	Subject    string   `json:"sub"`
	Audience   string   `json:"aud"`
	ActorType  string   `json:"actorType"`
	OnBehalfOf string   `json:"onBehalfOf,omitempty"`
	Username   string   `json:"preferred_username,omitempty"`
	Name       string   `json:"name,omitempty"`
	Roles      []string `json:"roles,omitempty"`
	SessionID  string   `json:"sessionId,omitempty"`
	Agent      string   `json:"agent,omitempty"`
	Model      string   `json:"model,omitempty"`
	IssuedAt   int64    `json:"iat"`
	ExpiresAt  int64    `json:"exp"`
	JTI        string   `json:"jti"`
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Signer mints tokens.
type Signer struct {
	key    ed25519.PrivateKey
	kid    string
	issuer string
	now    func() time.Time
}

// NewSigner builds a Signer.
func NewSigner(key ed25519.PrivateKey, kid, issuer string) *Signer {
	return &Signer{key: key, kid: kid, issuer: issuer, now: time.Now}
}

// PublicKey returns the verification key.
func (s *Signer) PublicKey() ed25519.PublicKey {
	pub, _ := s.key.Public().(ed25519.PublicKey)
	return pub
}

// Sign fills issuer, times and jti, then signs. ttl is capped by MaxTTL (an over-long ttl is an error).
func (s *Signer) Sign(c Claims, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > MaxTTL {
		return "", fmt.Errorf("internal token ttl must be in (0, %s]", MaxTTL)
	}
	if c.Subject == "" || c.Audience == "" {
		return "", errors.New("internal token needs sub and aud")
	}
	if c.ActorType != ActorHuman && c.ActorType != ActorAI && c.ActorType != ActorService {
		return "", errors.New("internal token has an invalid actorType")
	}
	now := s.now()
	c.Issuer = s.issuer
	c.IssuedAt = now.Unix()
	c.ExpiresAt = now.Add(ttl).Unix()
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	c.JTI = hex.EncodeToString(id[:])
	h, _ := json.Marshal(header{Alg: "EdDSA", Typ: "JWT", Kid: s.kid})
	p, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signing := b64(h) + "." + b64(p)
	return signing + "." + b64(ed25519.Sign(s.key, []byte(signing))), nil
}

// Verifier checks tokens minted by the edge.
type Verifier struct {
	pub    ed25519.PublicKey
	kid    string
	issuer string
	now    func() time.Time
}

// NewVerifier builds a Verifier.
func NewVerifier(pub ed25519.PublicKey, kid, issuer string) *Verifier {
	return &Verifier{pub: pub, kid: kid, issuer: issuer, now: time.Now}
}

// ErrInvalid is returned for every verification failure (no detail leaks to callers).
var ErrInvalid = errors.New("invalid internal token")

// Verify checks alg, kid, signature, issuer, audience and the time window.
func (v *Verifier) Verify(token, audience string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalid
	}
	hb, err := unb64(parts[0])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	var h header
	if json.Unmarshal(hb, &h) != nil || h.Alg != "EdDSA" || h.Kid != v.kid {
		return Claims{}, ErrInvalid
	}
	sig, err := unb64(parts[2])
	if err != nil || !ed25519.Verify(v.pub, []byte(parts[0]+"."+parts[1]), sig) {
		return Claims{}, ErrInvalid
	}
	pb, err := unb64(parts[1])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	var c Claims
	if json.Unmarshal(pb, &c) != nil {
		return Claims{}, ErrInvalid
	}
	now := v.now().Unix()
	if c.Issuer != v.issuer || c.Audience != audience || c.ExpiresAt <= now || c.IssuedAt > now+30 ||
		c.ExpiresAt-c.IssuedAt > int64(MaxTTL/time.Second) || c.Subject == "" {
		return Claims{}, ErrInvalid
	}
	return c, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// VerifierSet trusts several edges (gateway, mcp-server), each with its own key id, key and issuer.
type VerifierSet struct{ byKid map[string]*Verifier }

// NewVerifierSet builds a set; keys maps kid -> (public key, issuer).
func NewVerifierSet() *VerifierSet { return &VerifierSet{byKid: map[string]*Verifier{}} }

// Add registers an edge.
func (s *VerifierSet) Add(kid string, pub ed25519.PublicKey, issuer string) {
	s.byKid[kid] = NewVerifier(pub, kid, issuer)
}

// Verify selects the verifier by the (unauthenticated) kid header, then performs every check of Verifier.Verify.
func (s *VerifierSet) Verify(token, audience string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalid
	}
	hb, err := unb64(parts[0])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	var h header
	if json.Unmarshal(hb, &h) != nil {
		return Claims{}, ErrInvalid
	}
	v := s.byKid[h.Kid]
	if v == nil {
		return Claims{}, ErrInvalid
	}
	return v.Verify(token, audience)
}
