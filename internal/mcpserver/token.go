package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/gjolly/fleetmind/internal/snapconf"
)

// EnsureToken returns a bearer token, creating one on first run. Resolution
// order:
//
//  1. snap config key "token" (snapctl get token) if running in a snap;
//  2. $FLEETMIND_TOKEN if set;
//  3. otherwise a fresh 32-byte token is generated, persisted to snap config
//     (when in a snap) and mirrored to $SNAP_COMMON/token at mode 0600.
//
// The token itself is never logged.
func EnsureToken(ctx context.Context, log *slog.Logger) (string, error) {
	if tok, err := snapconf.Get(ctx, "token"); err == nil && tok != "" {
		return tok, nil
	} else if err != nil && !errors.Is(err, snapconf.ErrNotInSnap) {
		log.Warn("snapctl get token failed; falling back", "err", err)
	}

	if tok := os.Getenv("FLEETMIND_TOKEN"); tok != "" {
		return tok, nil
	}

	tok, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	if snapconf.InSnap() {
		if err := snapconf.Set(ctx, "token", tok); err != nil {
			return "", fmt.Errorf("persist token via snapctl: %w", err)
		}
		if common := os.Getenv("SNAP_COMMON"); common != "" {
			path := filepath.Join(common, "token")
			// path is rooted at the snap's per-instance writable dir owned by
			// snapd; no traversal possible from "token".
			if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil { //nolint:gosec // SNAP_COMMON-rooted path
				log.Warn("could not mirror token to SNAP_COMMON", "path", path, "err", err)
			}
		}
		log.Info("generated new bearer token", "hint", "run `snap get fleetmind token` to retrieve it")
	} else {
		log.Info("generated ephemeral bearer token (not running in a snap)",
			"hint", "set FLEETMIND_TOKEN to pin a value across restarts")
	}
	return tok, nil
}

func generateToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
