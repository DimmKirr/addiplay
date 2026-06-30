// Package creds persists the user's AudioAddict login (email, listen_key,
// session_key, premium) across runs. It prefers the OS keyring (Keychain
// on macOS, Secret Service on Linux, Wincred on Windows) and falls back
// to a chmod-600 JSON file under $XDG_CONFIG_HOME/addiplay when no
// keyring is available.
package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring" //nolint:gci // import order intentional
)

// Package-level debug sink. cmd/tui.go wires it to the --debug-log file
// via SetDebugLogger. Default discard, so production runs are silent.
var (
	debugLogMu sync.Mutex
	debugLog   io.Writer = io.Discard
)

// SetDebugLogger installs a writer for creds diagnostics. Pass nil/discard
// to disable. Safe to call concurrently.
func SetDebugLogger(w io.Writer) {
	debugLogMu.Lock()
	defer debugLogMu.Unlock()
	if w == nil {
		w = io.Discard
	}
	debugLog = w
}

func dlogf(format string, args ...any) {
	debugLogMu.Lock()
	w := debugLog
	debugLogMu.Unlock()
	_, _ = fmt.Fprintf(w, "[creds] "+format+"\n", args...)
}

// ErrNotLoggedIn means no credentials are saved.
var ErrNotLoggedIn = errors.New("creds: not logged in")

// service is the keyring service name; the keyring is keyed by service+account.
const service = "addiplay"

// account is the keyring account label. We use a single fixed value because
// the app supports a single AudioAddict user; the actual email lives inside
// the JSON-encoded payload.
const account = "default"

// Session is the persistent auth blob — every field returned by
// `/v1/<network>/member_sessions` that we care about plus the persistence
// metadata. Distinct from the wire format (parsed via authPayload in the
// audioaddict package); the same struct lives in keyring + creds.json.
//
// JSON tags are stable across versions. `id` was added 2026-06-29 for
// type unification with audioaddict.Member — old credfiles unmarshal it
// as 0, which is harmless.
type Session struct {
	ID         int64  `json:"id,omitempty"`
	Email      string `json:"email"`
	ListenKey  string `json:"listen_key"`
	SessionKey string `json:"session_key,omitempty"`
	Premium    bool   `json:"premium"`
}

// Load returns the saved Session, or ErrNotLoggedIn.
func Load() (Session, error) {
	// Keyring is the preferred source. If it returns anything other than
	// ErrNotFound we still fall through to the file fallback (the keyring
	// might be misconfigured but our chmod-600 file is always reachable).
	raw, kerr := keyring.Get(service, account)
	dlogf("Load: keyring.Get err=%v raw_len=%d", kerr, len(raw))
	if kerr == nil {
		var s Session
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			dlogf("Load: keyring decode FAIL err=%v", err)
			return Session{}, fmt.Errorf("decode keyring payload: %w", err)
		}
		dlogf("Load: keyring -> id=%d email_set=%t listen_key_len=%d session_key_len=%d premium=%t",
			s.ID, s.Email != "", len(s.ListenKey), len(s.SessionKey), s.Premium)
		return s, nil
	}

	path, err := filePath()
	if err != nil {
		dlogf("Load: filePath FAIL err=%v", err)
		return Session{}, err
	}
	dlogf("Load: falling back to file path=%s", path)
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			dlogf("Load: file does not exist -> ErrNotLoggedIn")
			return Session{}, ErrNotLoggedIn
		}
		dlogf("Load: file read FAIL err=%v", err)
		return Session{}, err
	}
	var s Session
	if err := json.Unmarshal(body, &s); err != nil {
		dlogf("Load: file decode FAIL err=%v body_len=%d", err, len(body))
		return Session{}, fmt.Errorf("decode creds file: %w", err)
	}
	dlogf("Load: file -> id=%d email_set=%t listen_key_len=%d session_key_len=%d premium=%t",
		s.ID, s.Email != "", len(s.ListenKey), len(s.SessionKey), s.Premium)
	return s, nil
}

// Save writes the session to keyring (preferred) and file (always, as a
// belt-and-braces fallback the user can see in their config dir).
func Save(s Session) error {
	dlogf("Save: id=%d email_set=%t listen_key_len=%d session_key_len=%d premium=%t",
		s.ID, s.Email != "", len(s.ListenKey), len(s.SessionKey), s.Premium)
	raw, err := json.Marshal(s)
	if err != nil {
		dlogf("Save: marshal FAIL err=%v", err)
		return err
	}
	keyringErr := keyring.Set(service, account, string(raw))
	dlogf("Save: keyring.Set err=%v raw_len=%d", keyringErr, len(raw))

	path, err := filePath()
	if err != nil {
		dlogf("Save: filePath FAIL err=%v", err)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		dlogf("Save: MkdirAll FAIL path=%s err=%v", filepath.Dir(path), err)
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		dlogf("Save: WriteFile FAIL path=%s err=%v", path, err)
		// If file write also fails AND keyring failed, return the file error.
		if keyringErr != nil {
			return fmt.Errorf("save creds: keyring=%v, file=%w", keyringErr, err)
		}
		return err
	}
	dlogf("Save: OK path=%s", path)
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

// Storage is the value form of the Save/Load/Clear funcs — it satisfies
// audioaddict.Storage (duck-typed; the iface lives in audioaddict to
// avoid a creds → audioaddict import cycle). Pass `creds.DefaultStorage`
// to `audioaddict.NewClient` so Authenticate persists every successful
// login automatically and Logout wipes the cache without the UI having
// to remember to call creds.Clear.
type Storage struct{}

// Save implements audioaddict.Storage.
func (Storage) Save(s Session) error { return Save(s) }

// Load implements audioaddict.Storage.
func (Storage) Load() (Session, error) { return Load() }

// Clear implements audioaddict.Storage.
func (Storage) Clear() error { return Clear() }

// DefaultStorage is the shared singleton wired by cmd/tui.go. Tests use a
// custom Storage implementation that doesn't touch the user's keyring.
var DefaultStorage = Storage{}
