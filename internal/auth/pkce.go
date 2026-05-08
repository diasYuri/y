package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
)

const codeVerifierCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

// GenerateCodeVerifier creates a random PKCE code verifier of the given length.
func GenerateCodeVerifier(length int) (string, error) {
	if length < 43 {
		length = 43
	}
	if length > 128 {
		length = 128
	}
	b := make([]byte, length)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = codeVerifierCharset[int(b[i])%len(codeVerifierCharset)]
	}
	return string(b), nil
}

// CodeChallenge computes the S256 code challenge from a verifier.
func CodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
