package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/fanart"
	"github.com/dimmkirr/addiplay/internal/player"
	"github.com/dimmkirr/addiplay/internal/ui"
)

// runTUI is the entry-point invoked when the user runs bare `addiplay`.
// It loads creds, refuses to launch the TUI without them (with a helpful
// hint), and otherwise starts the Bubble Tea program with real client +
// real mpv-backed player.
func runTUI(parent context.Context) error {
	// Load creds best-effort. If no session exists yet (ErrNotLoggedIn),
	// fall through with an empty Creds value; NewModel detects the empty
	// email/listen_key and auto-pops the login overlay so the user can
	// sign in without restarting the binary. Any other error (corrupted
	// keyring/file, permission denied) still fails fast — those aren't
	// fixable by signing in again.
	c, err := creds.Load()
	if err != nil && !errors.Is(err, creds.ErrNotLoggedIn) {
		return fmt.Errorf("load credentials: %w", err)
	}

	// Belt-and-braces ctx cancellation: the model also wraps with
	// WithCancel and calls m.cancel() in its quit handler, but if Bubble
	// Tea exits via a SIGINT path that bypasses our key handler, that
	// in-flight in-process cancel never fires. This defer runs the
	// MOMENT p.Run() returns, regardless of the exit path — every Cmd's
	// ctx is a descendant of this one, so they all get cancelled here.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	ApplyFanartFlags()
	dbg, closeDbg, err := openDebugLog()
	if err != nil {
		return err
	}
	defer closeDbg()
	// Pipe fanart diagnostics (URL, content-type, byte count, magic prefix)
	// into the debug log so decode failures are diagnosable. No-op when
	// --debug is off (dbg is nil → SetDebugLogger(io.Discard equivalent)).
	if dbg != nil {
		fanart.SetDebugLogger(dbg.Writer)
		ui.SetDebugLogger(dbg.Writer)
		creds.SetDebugLogger(dbg.Writer)
		defer fanart.SetDebugLogger(nil)
		defer ui.SetDebugLogger(nil)
		defer creds.SetDebugLogger(nil)
		_, _ = fmt.Fprintf(dbg.Writer,
			"[tui] runTUI starting (load_err=%v email_set=%t listen_key_len=%d session_key_len=%d premium=%t)\n",
			err, c.Email != "", len(c.ListenKey), len(c.SessionKey), c.Premium)
	}

	// Best-effort startup cache sweep — enforces the 30d-age / 50 MiB
	// size limits before the model wires up image+API fetches that
	// will write fresh entries. Failures are logged and ignored: a
	// corrupt cache should never prevent boot.
	if dbg != nil {
		sweepCacheBestEffort(dbg.Writer)
	} else {
		sweepCacheBestEffort(nil)
	}

	client := audioaddict.NewClient(creds.DefaultStorage)
	client.SetCreds(c)
	if dbg != nil {
		client.Debug = dbg.Writer
	}
	newPlayer := func(ctx context.Context) (ui.AudioPlayer, error) {
		var opts []player.Option
		if dbg != nil {
			opts = append(opts,
				player.WithDebugWriter(dbg.Writer),
				player.WithMPVLogFile(dbg.MPVLogPath),
			)
		}
		return player.New(ctx, opts...)
	}

	p := tea.NewProgram(
		ui.NewModel(ctx, c, client, newPlayer),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	if dbg != nil {
		_, _ = fmt.Fprintln(dbg.Writer, "[tui] p.Run() entering")
	}
	_, err = p.Run()
	if dbg != nil {
		_, _ = fmt.Fprintf(dbg.Writer, "[tui] p.Run() returned (err=%v)\n", err)
	}

	// Flush any Kitty graphics still in the terminal's image storage.
	// Per the protocol, `a=T,U=1` puts the image bytes in storage and
	// the placeholder cells reference it. Exiting alt-screen removes
	// the placeholders, but the storage hangs around — and on some
	// terminal states (Ctrl-C during a render, passthrough quirks)
	// stale images leak onto the user's shell. `a=d,d=A` deletes all
	// images and their placements. Harmless on non-Kitty terminals
	// (escape is ignored). User report: Kitty image "got stale on
	// ctrl+c" — this is the cleanup that was missing.
	_, _ = fmt.Fprint(os.Stdout, "\x1b_Ga=d,d=A;\x1b\\")
	if dbg != nil {
		_, _ = fmt.Fprintln(dbg.Writer, "[tui] Kitty image storage cleared; runTUI returning")
	}

	return err
}
