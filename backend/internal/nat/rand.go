package nat

import "crypto/rand"

// readRand fills b with cryptographically random bytes; STUN transaction IDs
// only need to be unpredictable enough to avoid cross-response collisions.
func readRand(b []byte) (int, error) { return rand.Read(b) }
