package agentauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const Prefix = "migi_at_"

// Generate creates a token whose short public ID can be used for indexed
// lookup while only the SHA-256 hash of the complete credential is persisted.
func Generate() (tokenID string, plain string, tokenHash [sha256.Size]byte, err error) {
	idBytes := make([]byte, 9)
	secret := make([]byte, 32)
	if _, err = rand.Read(idBytes); err != nil {
		return "", "", tokenHash, err
	}
	if _, err = rand.Read(secret); err != nil {
		return "", "", tokenHash, err
	}
	tokenID = hex.EncodeToString(idBytes)
	plain = Prefix + tokenID + "_" + base64.RawURLEncoding.EncodeToString(secret)
	tokenHash = sha256.Sum256([]byte(plain))
	return tokenID, plain, tokenHash, nil
}

// Parse validates the credential shape and returns its public ID and hash.
func Parse(plain string) (string, [sha256.Size]byte, bool) {
	var tokenHash [sha256.Size]byte
	rest, ok := strings.CutPrefix(plain, Prefix)
	if !ok {
		return "", tokenHash, false
	}
	tokenID, secretText, ok := strings.Cut(rest, "_")
	if !ok || tokenID == "" || secretText == "" {
		return "", tokenHash, false
	}
	idBytes, err := hex.DecodeString(tokenID)
	if err != nil || len(idBytes) != 9 {
		return "", tokenHash, false
	}
	secret, err := base64.RawURLEncoding.DecodeString(secretText)
	if err != nil || len(secret) != 32 {
		return "", tokenHash, false
	}
	tokenHash = sha256.Sum256([]byte(plain))
	return tokenID, tokenHash, true
}
