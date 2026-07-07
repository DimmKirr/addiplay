package creds_test

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/dimmkirr/addiplay/internal/creds"
)

func TestSaveLoadClear_roundtrip(t *testing.T) {
	keyring.MockInit()
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())

	want := creds.Session{Email: "me@example.com", ListenKey: "abc", Premium: true}
	if err := creds.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := creds.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if err := creds.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Load(); !errors.Is(err, creds.ErrNotLoggedIn) {
		t.Errorf("after Clear: err=%v, want ErrNotLoggedIn", err)
	}
}

func TestLoad_notLoggedIn(t *testing.T) {
	keyring.MockInit()
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())

	_, err := creds.Load()
	if !errors.Is(err, creds.ErrNotLoggedIn) {
		t.Errorf("err = %v, want ErrNotLoggedIn", err)
	}
}

// TestSaveLoad_persistsSessionKey covers DIMM-381: SessionKey (used to
// authenticate vote/like calls via X-Session-Key) must round-trip alongside
// the listen_key.
func TestSaveLoad_persistsSessionKey(t *testing.T) {
	keyring.MockInit()
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())

	want := creds.Session{
		Email:      "me@example.com",
		ListenKey:  "lk-abc",
		SessionKey: "sk-xyz",
		Premium:    true,
	}
	if err := creds.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := creds.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionKey != "sk-xyz" {
		t.Errorf("SessionKey = %q, want %q", got.SessionKey, "sk-xyz")
	}
}

// TestLoad_oldCredfileWithoutSessionKey covers the upgrade path: users who
// authed before DIMM-381 have a credfile with no `session_key` field. Load
// must succeed and return an empty SessionKey rather than failing JSON
// decode — the UI layer detects the empty value and triggers a re-auth on
// the first `l` press.
func TestLoad_oldCredfileWithoutSessionKey(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set("addiplay", "default",
		`{"email":"old@y","listen_key":"old-lk","premium":false}`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())

	got, err := creds.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ListenKey != "old-lk" {
		t.Errorf("ListenKey = %q, want old-lk", got.ListenKey)
	}
	if got.SessionKey != "" {
		t.Errorf("SessionKey = %q, want empty for old creds", got.SessionKey)
	}
}

func TestLoad_keyringPreferred(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("ADDICTUNED_CONFIG_DIR", dir)

	// Write to keyring directly with a value that differs from file.
	if err := keyring.Set("addiplay", "default", `{"email":"k@y","listen_key":"K","premium":false}`); err != nil {
		t.Fatal(err)
	}
	got, err := creds.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.ListenKey != "K" {
		t.Errorf("listen_key = %q, want K (keyring should be preferred)", got.ListenKey)
	}
}
