package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestGenerateCodeVerifierLength(t *testing.T) {
	for _, length := range []int{40, 43, 64, 128, 130} {
		v, err := GenerateCodeVerifier(length)
		if err != nil {
			t.Fatalf("length %d: %v", length, err)
		}
		want := length
		if want < 43 {
			want = 43
		}
		if want > 128 {
			want = 128
		}
		if len(v) != want {
			t.Errorf("length %d: got %d, want %d", length, len(v), want)
		}
		for i, c := range v {
			ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~'
			if !ok {
				t.Errorf("length %d: invalid char %q at pos %d", length, c, i)
			}
		}
	}
}

func TestCodeChallenge(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := CodeChallenge(verifier)

	h := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(h[:])

	if challenge != want {
		t.Errorf("challenge mismatch: got %q, want %q", challenge, want)
	}
}

func TestCodeChallengeUniqueness(t *testing.T) {
	v1, _ := GenerateCodeVerifier(43)
	v2, _ := GenerateCodeVerifier(43)
	c1 := CodeChallenge(v1)
	c2 := CodeChallenge(v2)
	if c1 == c2 {
		t.Error("different verifiers produced same challenge")
	}
}
