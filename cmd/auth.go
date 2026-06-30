package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/dimmkirr/addiplay/internal/creds"
)

// Sign-in flow is the TUI overlay (internal/ui/screen_login.go) —
// auto-popped on first run or 401. The former `addiplay login` CLI
// prompt is gone. The two helpers here back the `--logout` and
// `--whoami` action flags wired in cmd/root.go.

// runLogout clears stored credentials. After this, launching `addiplay`
// will trigger the TUI login overlay on next start.
func runLogout(out io.Writer) error {
	if err := creds.Clear(); err != nil {
		return fmt.Errorf("clear credentials: %w", err)
	}
	_, _ = fmt.Fprintln(out, "logged out")
	return nil
}

// runWhoami prints the saved AudioAddict account email + premium flag,
// or a friendly hint if no session exists yet.
func runWhoami(out io.Writer) error {
	got, err := creds.Load()
	if errors.Is(err, creds.ErrNotLoggedIn) {
		_, _ = fmt.Fprintln(out, "not signed in — launch `addiplay` to sign in via the TUI overlay")
		return nil
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "%s (premium=%t)\n", got.Email, got.Premium)
	return nil
}
