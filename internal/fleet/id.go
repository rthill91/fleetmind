package fleet

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewNodeID returns a 16-byte random hex string used as a stable identifier
// for a fleetmind process instance. Generated once at startup; not persisted
// across restarts.
func NewNodeID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
