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

	want := creds.Creds{Email: "me@example.com", ListenKey: "abc", Premium: true}
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
