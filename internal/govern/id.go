package govern

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a random 16-hex-char request/trace id.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-00000000"
	}
	return hex.EncodeToString(b[:])
}
