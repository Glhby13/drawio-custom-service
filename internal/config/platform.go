package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// TrustedKey is an edge (gateway, mcp-server) whose internal tokens the platform accepts.
type TrustedKey struct {
	Kid    string
	Pub    ed25519.PublicKey
	Issuer string
}

// Platform holds every setting of the platform deployable (documents + authz + oplog + collab + audit).
type Platform struct {
	Port                                                     string
	PGHost, PGPort, PGDB, PGUser, PGPassword, PGSSLMode      string
	S3Endpoint, S3Bucket, S3AccessKey, S3SecretKey, S3Region string
	S3UseSSL                                                 bool
	RedisAddr, RedisUser, RedisPassword                      string
	CAFile                                                   string
	Trusted                                                  []TrustedKey
	SnapshotEvery                                            int64
	AdminRole, UserRole                                      string
	AIDefaultMaxOpsPerMin, AIDefaultMaxDelete                int
	PublicURL                                                string
	LocalAdminUser, LocalAdminPassword                       string // optional bootstrap of the first local admin (only when none exists)
}

// LoadPlatform reads and validates the environment.
func LoadPlatform(getenv func(string) string) (Platform, error) {
	p := Platform{
		Port: or(getenv("PORT"), "8080"), PGHost: getenv("PG_HOST"), PGPort: or(getenv("PG_PORT"), "5432"), PGDB: or(getenv("PG_DB"), "drawio_db"),
		PGUser: getenv("PG_USER"), PGPassword: getenv("PG_PASSWORD"), PGSSLMode: or(getenv("PG_SSLMODE"), "disable"),
		S3Endpoint: getenv("S3_ENDPOINT"), S3Bucket: or(getenv("S3_BUCKET"), "drawio-documents"), S3AccessKey: getenv("S3_ACCESS_KEY"), S3SecretKey: getenv("S3_SECRET_KEY"),
		S3Region: or(getenv("S3_REGION"), "us-east-1"), S3UseSSL: getenv("S3_USE_SSL") == "true",
		RedisAddr: getenv("REDIS_ADDR"), RedisUser: getenv("REDIS_USER"), RedisPassword: getenv("REDIS_PASSWORD"), CAFile: getenv("EXTRA_CA_FILE"),
		AdminRole: or(getenv("ADMIN_ROLE"), "admin"), UserRole: or(getenv("REQUIRED_ROLE"), "user"), PublicURL: getenv("PUBLIC_URL"), LocalAdminUser: or(getenv("LOCAL_ADMIN_USERNAME"), "admin"), LocalAdminPassword: getenv("LOCAL_ADMIN_PASSWORD"),
		SnapshotEvery: 200, AIDefaultMaxOpsPerMin: 300, AIDefaultMaxDelete: 20,
	}
	var errs []error
	for name, v := range map[string]string{"PG_HOST": p.PGHost, "PG_USER": p.PGUser, "PG_PASSWORD": p.PGPassword, "S3_ENDPOINT": p.S3Endpoint,
		"S3_ACCESS_KEY": p.S3AccessKey, "S3_SECRET_KEY": p.S3SecretKey, "REDIS_ADDR": p.RedisAddr, "INTERNAL_KEYS": getenv("INTERNAL_KEYS")} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	if v := getenv("SNAPSHOT_EVERY"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 10 {
			errs = append(errs, errors.New("SNAPSHOT_EVERY must be an integer >= 10"))
		} else {
			p.SnapshotEvery = n
		}
	}
	keys, err := ParseTrustedKeys(getenv("INTERNAL_KEYS"))
	errs = append(errs, err)
	p.Trusted = keys
	if e := errors.Join(errs...); e != nil {
		return Platform{}, e
	}
	return p, nil
}

// ParseTrustedKeys parses "kid=<base64 ed25519 public key>@<issuer>,kid2=...".
func ParseTrustedKeys(raw string) ([]TrustedKey, error) {
	var out []TrustedKey
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		kid, rest, ok := strings.Cut(item, "=")
		b64, iss, ok2 := strings.Cut(rest, "@")
		if !ok || !ok2 || kid == "" || iss == "" {
			return nil, fmt.Errorf("INTERNAL_KEYS entry %q must look like kid=<base64 public key>@<issuer>", item)
		}
		pub, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("INTERNAL_KEYS %q: public key must be base64 of 32 bytes", kid)
		}
		out = append(out, TrustedKey{Kid: kid, Pub: pub, Issuer: iss})
	}
	if len(out) == 0 {
		return nil, errors.New("INTERNAL_KEYS lists no key")
	}
	return out, nil
}
