// Package creds persists the user's AudioAddict login (email + listen_key)
// across runs. It prefers the OS keyring (Keychain on macOS, Secret Service
// on Linux, Wincred on Windows) and falls back to a chmod-600 JSON file
// under $XDG_CONFIG_HOME/addiplay when no keyring is available.
package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring" //nolint:gci // import order intentional
)

// ErrNotLoggedIn means no credentials are saved.
var ErrNotLoggedIn = errors.New("creds: not logged in")

// service is the keyring service name; the keyring is keyed by service+account.
const service = "addiplay"

// account is the keyring account label. We use a single fixed value because
// the app supports a single AudioAddict user; the actual email lives inside
// the JSON-encoded payload.
const account = "default"

// Creds is the persisted blob.
type Creds struct {
	Email     string `json:"email"`
	ListenKey string `json:"listen_key"`
	Premium   bool   `json:"premium"`
}

// Load returns the saved Creds, or ErrNotLoggedIn.
func Load() (Creds, error) {
	// Keyring is the preferred source. If it returns anything other than
	// ErrNotFound we still fall through to the file fallback (the keyring
	// might be misconfigured but our chmod-600 file is always reachable).
	if raw, err := keyring.Get(service, account); err == nil {
		var c Creds
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return Creds{}, fmt.Errorf("decode keyring payload: %w", err)
		}
		return c, nil
	}

	path, err := filePath()
	if err != nil {
		return Creds{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Creds{}, ErrNotLoggedIn
		}
		return Creds{}, err
	}
	var c Creds
	if err := json.Unmarshal(raw, &c); err != nil {
		return Creds{}, fmt.Errorf("decode creds file: %w", err)
	}
	return c, nil
}

// Save writes the creds to keyring (preferred) and file (always, as a
// belt-and-braces fallback the user can see in their config dir).
func Save(c Creds) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	keyringErr := keyring.Set(service, account, string(raw))

	path, err := filePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		// If file write also fails AND keyring failed, return the file error.
		if keyringErr != nil {
			return fmt.Errorf("save creds: keyring=%v, file=%w", keyringErr, err)
		}
		return err
	}
	return nil
}

// Clear removes the saved credentials from both backends.
func Clear() error {
	_ = keyring.Delete(service, account)
	path, err := filePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// filePath returns the fallback file location: $XDG_CONFIG_HOME/addiplay/creds.json.
// Honors ADDICTUNED_CONFIG_DIR (set by tests) for isolation.
func filePath() (string, error) {
	if override := os.Getenv("ADDICTUNED_CONFIG_DIR"); override != "" {
		return filepath.Join(override, "creds.json"), nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "addiplay", "creds.json"), nil
}
