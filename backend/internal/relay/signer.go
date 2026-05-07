package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type Signer struct {
	secret []byte
}

func NewSigner(secret string) Signer {
	return Signer{secret: []byte(secret)}
}

func (s Signer) Ref(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s Signer) Sign(ref string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(ref))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s Signer) Verify(ref, signature string) bool {
	expected := s.Sign(ref)
	return hmac.Equal([]byte(expected), []byte(signature))
}

// SignPlayback binds slug + absolute exp (unix seconds) for share links.
func (s Signer) SignPlayback(slug string, expUnix int64) string {
	return s.Sign(fmt.Sprintf("play|%s|%d", slug, expUnix))
}

// VerifyPlayback verifies a share-link signature in constant time.
func (s Signer) VerifyPlayback(slug, signature string, expUnix int64) bool {
	return s.Verify(fmt.Sprintf("play|%s|%d", slug, expUnix), signature)
}
