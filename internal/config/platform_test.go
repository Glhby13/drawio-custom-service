package config

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func platformEnv(over map[string]string) func(string) string {
	m := map[string]string{
		"PG_HOST": "pg", "PG_USER": "u", "PG_PASSWORD": "p", "S3_ENDPOINT": "s3:9000", "S3_ACCESS_KEY": "a", "S3_SECRET_KEY": "s",
		"REDIS_ADDR":    "redis:6379",
		"INTERNAL_KEYS": "k-1=" + base64.StdEncoding.EncodeToString(make([]byte, 32)) + "@edge",
	}
	for k, v := range over {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func TestInviteTokenKey(t *testing.T) {
	p, err := LoadPlatform(platformEnv(nil))
	if err != nil || p.InviteTokenKey != nil {
		t.Fatalf("empty key: %v %v", err, p.InviteTokenKey)
	}
	good := bytes.Repeat([]byte{7}, 32)
	p, err = LoadPlatform(platformEnv(map[string]string{"INVITE_TOKEN_KEY": base64.StdEncoding.EncodeToString(good)}))
	if err != nil || !bytes.Equal(p.InviteTokenKey, good) {
		t.Fatalf("32-byte key: %v %v", err, p.InviteTokenKey)
	}
	long := bytes.Repeat([]byte{9}, 48)
	if p, err = LoadPlatform(platformEnv(map[string]string{"INVITE_TOKEN_KEY": base64.StdEncoding.EncodeToString(long)})); err != nil || len(p.InviteTokenKey) != 48 {
		t.Fatalf("48-byte key: %v", err)
	}
	for name, v := range map[string]string{
		"16 bytes":       base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"invalid base64": "not*base64!!",
	} {
		if _, err := LoadPlatform(platformEnv(map[string]string{"INVITE_TOKEN_KEY": v})); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}
