package platform

import (
	"strings"
	"testing"
)

func TestPasswordHashing(t *testing.T) {
	h, err := hashPassword("correct horse battery")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash %q %v", h, err)
	}
	h2, _ := hashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salts must differ")
	}
	if !verifyPassword(h, "correct horse battery") || verifyPassword(h, "correct horse batterY") || verifyPassword(h, "") {
		t.Fatal("verify")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$AAAA", "$bcrypt$x$y$z$w$v", "$argon2id$v=19$m=99999999,t=2,p=1$AAAA$AAAA"} {
		if verifyPassword(bad, "x") {
			t.Fatalf("garbage hash accepted: %q", bad)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	for pw, ok := range map[string]bool{"short": false, "exactly10!": true, strings.Repeat("x", 129): false, strings.Repeat("é", 128): true, "SameAsName1": false} {
		err := validPassword(pw, "samename1")
		if (err == nil) != ok && pw != "SameAsName1" {
			t.Errorf("%.12q: err=%v", pw, err)
		}
	}
	if validPassword("SameAsName1", "sameasname1") == nil {
		t.Error("a password equal to the username must be refused (case-insensitive)")
	}
	for u, ok := range map[string]bool{"abc": true, "a.b-c_d": true, "ab": false, "Abc": false, "-abc": false, "a b c": false, strings.Repeat("a", 33): false, strings.Repeat("a", 32): true} {
		if usernameRe.MatchString(u) != ok {
			t.Errorf("username %q: want %v", u, ok)
		}
	}
}
