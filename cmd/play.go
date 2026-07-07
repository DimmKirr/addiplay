package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/player"
)

// runHeadlessPlay backs `addiplay --play <network>/<channel>`. Spins up
// mpv against the resolved stream URL, blocks until Ctrl-C / SIGTERM /
// 24 h timeout, then stops cleanly. No TUI, no input handling — useful
// for scripts and smoke tests.
//
// Requires a saved listen_key. If creds are absent, prints a hint and
// exits non-zero — sign in via the TUI overlay first (`addiplay` with
// no args).
func runHeadlessPlay(ctx context.Context, target string, out io.Writer) error {
	network, channel, ok := strings.Cut(target, "/")
	if !ok || network == "" || channel == "" {
		return fmt.Errorf("--play expects <network>/<channel>, got %q", target)
	}

	ApplyFanartFlags()
	dbg, closeDbg, err := openDebugLog()
	if err != nil {
		return err
	}
	defer closeDbg()

	// Best-effort startup cache sweep — same rationale as runTUI.
	if dbg != nil {
		sweepCacheBestEffort(dbg.Writer)
	} else {
		sweepCacheBestEffort(nil)
	}

	got, err := creds.Load()
	if errors.Is(err, creds.ErrNotLoggedIn) {
		fmt.Fprintln(os.Stderr, "not signed in — launch `addiplay` and use the TUI login overlay, then retry")
		os.Exit(2)
	}
	if err != nil {
		return err
	}

	client := audioaddict.NewClient(creds.DefaultStorage)
	client.SetCreds(got)
	streamURL, err := client.StreamURL(ctx, network, channel, audioaddict.QualityPremiumHigh)
	if err != nil {
		return fmt.Errorf("resolve stream url: %w", err)
	}

	var popts []player.Option
	if dbg != nil {
		popts = append(popts,
			player.WithDebugWriter(dbg.Writer),
			player.WithMPVLogFile(dbg.MPVLogPath),
		)
	}
	pl, err := player.New(ctx, popts...)
	if err != nil {
		return fmt.Errorf("start player: %w", err)
	}
	defer func() { _ = pl.Close() }()

	if err := pl.Play(streamURL); err != nil {
		return fmt.Errorf("play: %w", err)
	}
	_, _ = fmt.Fprintf(out, "playing %s/%s — press Ctrl-C to stop\n", network, channel)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
	case <-ctx.Done():
	case <-time.After(24 * time.Hour):
	}
	return pl.Stop()
}
