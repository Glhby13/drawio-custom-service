package totp

import (
	"strings"
	"testing"
)

func TestTOTPGenerationAndValidation(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret failed: %v", err)
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
