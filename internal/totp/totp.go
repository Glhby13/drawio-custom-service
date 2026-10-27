package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 specifies HMAC-SHA1 as the default standard for TOTP
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
)

const (
	period = 30
	digits = 6
)

// GenerateSecret creates a 20-byte cryptographically secure random base32-encoded secret.
func GenerateSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	// RFC 4648 base32 without padding
	return strings.TrimRight(base32.StdEncoding.EncodeToString(buf), "="), nil
}

// Validate checks a 6-digit TOTP code against a base32 secret with a +/- 1 step (30s) tolerance.
func Validate(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != digits {
		return false
	}
	// Add padding if missing
	s := strings.ToUpper(strings.TrimSpace(secret))
	if pad := len(s) % 8; pad != 0 {
		s += strings.Repeat("=", 8-pad)
	}
	key, err := base32.StdEncoding.DecodeString(s)
	if err != nil || len(key) == 0 {
		return false
	}

	now := time.Now().Unix()
	step := now / period

	// Check previous, current, and next time windows
	for _, offset := range []int64{-1, 0, 1} {
		t := step + offset
		if computeCode(key, t) == code {
			return true
		}
	}
	return false
}

func computeCode(key []byte, counter int64) string {
	if counter < 0 {
		return ""
	}
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter)) //nolint:gosec // counter is checked >= 0

	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(buf)
	h := mac.Sum(nil)

	offset := h[len(h)-1] & 0x0f
	binCode := (binary.BigEndian.Uint32(h[offset:offset+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", binCode)
}

// GenerateCode calculates the current 6-digit code for a given base32 secret.
func GenerateCode(secret string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(secret))
	if pad := len(s) % 8; pad != 0 {
		s += strings.Repeat("=", 8-pad)
	}
	key, err := base32.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	step := time.Now().Unix() / period
	return computeCode(key, step), nil
}

// QRCodeBase64 generates a data:image/png;base64,... URI for the authenticator setup.
func QRCodeBase64(secret, username, issuer string) (string, error) {
	label := username
	if issuer != "" {
		label = issuer + ":" + username
	}
	uri := fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		url.PathEscape(label),
		url.QueryEscape(strings.ToUpper(strings.TrimSpace(secret))),
		url.QueryEscape(issuer),
		digits,
		period,
	)

	pngBytes, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes), nil
}
