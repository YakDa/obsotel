package obs

import (
	"crypto/rand"
	"encoding/hex"
)

// NewRequestID returns a 32-character hex string (16 random bytes).
// Not a strict UUID v4 layout, but cryptographically random and unique
// enough for log correlation. Avoids the github.com/google/uuid dep.
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand fails only if the OS is broken; static fallback
		// keeps the program running instead of panicking on a logger.
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}
