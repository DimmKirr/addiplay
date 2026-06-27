package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/fanart"
)

// runDoctor backs `addiplay --doctor`. Prints a status table for every
// external thing addiplay depends on, so you can spot misconfiguration
// without spelunking the source.
//
// What gets checked:
//   - mpv binary on PATH (required for playback)
//   - Terminal image protocol support (Ghostty / Kitty / Wezterm / Foot)
//     — including the tmux passthrough trap when running inside tmux
//   - Credentials (loaded from OS keyring or fallback file)
//   - AudioAddict network reachability (one quick GET)
//
// Each line is either ✓ (working) or ✗ (broken) with a one-line "what to do".
func runDoctor(ctx context.Context, o io.Writer) error {
	_, _ = fmt.Fprintln(o, "addiplay doctor")
	_, _ = fmt.Fprintln(o, strings.Repeat("─", 60))

	// 1. mpv
	if path, err := exec.LookPath("mpv"); err == nil {
		_, _ = fmt.Fprintf(o, "✓  mpv:        %s\n", path)
	} else {
		_, _ = fmt.Fprintln(o, "✗  mpv:        not found in PATH")
		_, _ = fmt.Fprintln(o, "              install: brew install mpv  (mac)  /  apt install mpv  (linux)")
	}

	// 2. Terminal image support
	term := os.Getenv("TERM")
	tp := os.Getenv("TERM_PROGRAM")
	inTmux := strings.HasPrefix(term, "tmux") || strings.HasPrefix(term, "screen")

	_, _ = fmt.Fprintf(o, "·  TERM:       %s\n", term)
	if tp != "" {
		_, _ = fmt.Fprintf(o, "·  TERM_PROGRAM: %s\n", tp)
	}
	if v := os.Getenv("KITTY_WINDOW_ID"); v != "" {
		_, _ = fmt.Fprintf(o, "·  KITTY_WINDOW_ID: %s\n", v)
	}
	if v := os.Getenv("GHOSTTY_RESOURCES_DIR"); v != "" {
		_, _ = fmt.Fprintf(o, "·  GHOSTTY_RESOURCES_DIR: %s\n", v)
	}
	if v := os.Getenv("ADDIPLAY_FORCE_FANART"); v != "" {
		_, _ = fmt.Fprintf(o, "·  ADDIPLAY_FORCE_FANART: %s (override)\n", v)
	}
	if v := os.Getenv("ADDIPLAY_NO_FANART"); v != "" {
		_, _ = fmt.Fprintf(o, "·  ADDIPLAY_NO_FANART: %s (override)\n", v)
	}

	ApplyFanartFlags()
	mode := fanart.DetectMode()
	switch mode {
	case fanart.ModeKitty:
		_, _ = fmt.Fprintln(o, "✓  fanart:     Kitty graphics protocol (best quality, real pixels)")
	case fanart.ModeASCII:
		_, _ = fmt.Fprintln(o, "✓  fanart:     truecolor ASCII half-blocks (works in tmux, ssh, etc.)")
	default:
		_, _ = fmt.Fprintln(o, "✗  fanart:     no compatible rendering mode — no channel art")
		switch {
		case os.Getenv("ADDIPLAY_NO_FANART") != "":
			_, _ = fmt.Fprintln(o, "              ADDIPLAY_NO_FANART is set — unset to enable")
		case inTmux:
			_, _ = fmt.Fprintln(o, "              inside tmux without graphics passthrough; ASCII would work")
			_, _ = fmt.Fprintln(o, "              if your terminal had COLORTERM=truecolor set")
		default:
			_, _ = fmt.Fprintln(o, "              terminal advertises neither Kitty graphics nor truecolor (COLORTERM)")
		}
	}

	// 3. Credentials
	ch, err := creds.Load()
	switch {
	case err != nil && strings.Contains(err.Error(), "not logged in"):
		_, _ = fmt.Fprintln(o, "✗  creds:      not logged in")
		_, _ = fmt.Fprintln(o, "              fix: launch `addiplay` and sign in via the TUI overlay")
	case err != nil:
		_, _ = fmt.Fprintf(o, "✗  creds:      %v\n", err)
	default:
		premium := "free"
		if ch.Premium {
			premium = "premium"
		}
		_, _ = fmt.Fprintf(o, "✓  creds:      %s (%s, listen_key prefix=%s…)\n",
			ch.Email, premium, prefix(ch.ListenKey, 6))
	}

	// 4. API reachability — cheap GET against the public channels list
	apiCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := audioaddict.NewClient()
	channels, apiErr := client.Channels(apiCtx, "di")
	switch {
	case apiErr != nil:
		_, _ = fmt.Fprintf(o, "✗  api:        unreachable: %v\n", apiErr)
	default:
		_, _ = fmt.Fprintf(o, "✓  api:        api.audioaddict.com OK (%d di channels)\n", len(channels))
		// Sample a channel to verify Image.Vertical wiring
		for _, c := range channels {
			if c.Image.Vertical != "" {
				url := audioaddict.ResolveImageURL(c.Image.Vertical, 300, 280, 75)
				_, _ = fmt.Fprintf(o, "·  sample art: %s\n", url)
				break
			}
		}
	}

	_, _ = fmt.Fprintln(o, strings.Repeat("─", 60))
	return nil
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
