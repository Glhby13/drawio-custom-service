package totp

import (
	"encoding/base32"
	"strings"
	"testing"
)

func TestTOTPGenerationAndValidation(t *testing.T) {
	_, secret, err := NewKeycloakSecret()
	if err != nil {
		t.Fatalf("NewKeycloakSecret failed: %v", err)
	}
	if len(secret) < 16 {
		t.Fatalf("Secret too short: %s", secret)
	}

	code, err := GenerateCode(secret)
	if err != nil {
		t.Fatalf("GenerateCode failed: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("Code length not 6: %s", code)
	}

	if !Validate(secret, code) {
		t.Fatalf("Validation failed for freshly generated code: %s", code)
	}

	if Validate(secret, "000000") && code != "000000" {
		t.Fatalf("Validation should fail for wrong code")
	}

	qr, err := QRCodeBase64(secret, "admin", "DrawSpace")
	if err != nil {
		t.Fatalf("QRCodeBase64 failed: %v", err)
	}
	if !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Fatalf("Invalid QR code prefix: %s", qr[:30])
	}
}

func TestNewKeycloakSecretAgreesInBothForms(t *testing.T) {
	raw, b32, err := NewKeycloakSecret()
	if err != nil || len(raw) != 20 {
		t.Fatalf("secret: %q %v", raw, err)
	}
	code, err := GenerateCode(b32)
	if err != nil || !Validate(b32, code) {
		t.Fatalf("code from the base32 form must validate: %v", err)
	}
	// the raw string's own bytes are the key, which is how Keycloak reads secretData
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32)
	if err != nil || string(key) != raw {
		t.Fatalf("base32 form must encode the raw bytes: %v", err)
	}
	other, _, _ := NewKeycloakSecret()
	if other == raw {
		t.Fatal("secrets must be random")
	}
}
