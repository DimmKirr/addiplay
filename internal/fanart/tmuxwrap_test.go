package fanart

import (
	"strings"
	"testing"
)

func truncForTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestEncodePlaceholder_insideTmux_wrapsTransmitForPassthrough is the
// DIMM-420 #2 root-cause test. Inside tmux, bare `\x1b_G...` APC
// escapes are consumed by tmux and never reach the host terminal —
// even with `allow-passthrough on`, tmux requires the escape to be
// wrapped in `\x1bPtmux;...\x1b\\` (with inner ESCs doubled). Without
// the wrap, the Kitty transmit silently disappears: the terminal
// never stores the image, the placeholder cells render nothing.
//
// This test forces inTmux()=true via TERM and asserts the encoded
// output uses tmux passthrough format around every APC chunk.
func TestEncodePlaceholder_insideTmux_wrapsTransmitForPassthrough(t *testing.T) {
	t.Setenv("TERM", "tmux-256color")

	// Tiny PNG payload — content doesn't matter, only the wrapping.
	imgBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x01}
	out := EncodePlaceholder(imgBytes, 4, 2, 101)

	if !strings.Contains(out, "\x1bPtmux;") {
		t.Errorf("inside tmux, output missing \\x1bPtmux; passthrough opener — terminal will never see the transmit\noutput prefix (200 bytes): %q",
			truncForTest(out, 200))
	}
	// Inside the wrapper, the inner ESC of the Kitty APC must be
	// doubled to \x1b\x1b (per tmux passthrough spec). A bare
	// \x1b_G at the START of the byte stream would mean the wrap
	// wasn't applied.
	if strings.HasPrefix(out, "\x1b_G") {
		t.Error("output begins with bare \\x1b_G — tmux wrap not applied")
	}
	// The wrap should contain \x1b\x1b_G (the doubled inner ESC).
	if !strings.Contains(out, "\x1b\x1b_G") {
		t.Errorf("expected doubled-ESC inner APC `\\x1b\\x1b_G` inside the tmux wrap\noutput prefix: %q", truncForTest(out, 200))
	}
}

// TestEncodePlaceholder_outsideTmux_emitsRawTransmit pins the inverse:
// without tmux, the output must NOT be wrapped — wrapping would
// confuse the host terminal into expecting a tmux passthrough payload.
func TestEncodePlaceholder_outsideTmux_emitsRawTransmit(t *testing.T) {
	t.Setenv("TERM", "xterm-kitty")

	imgBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x01}
	out := EncodePlaceholder(imgBytes, 4, 2, 101)

	if strings.Contains(out, "\x1bPtmux;") {
		t.Error("outside tmux, output should NOT contain tmux passthrough wrapper")
	}
	if !strings.HasPrefix(out, "\x1b_G") {
		t.Errorf("outside tmux, expected bare \\x1b_G transmit prefix\nprefix: %q", truncForTest(out, 80))
	}
}

// TestEncode_insideTmux_wrapsForPassthrough — same guarantee for the
// direct-placement Encode() used by the Now Playing pane in Kitty mode.
// The "wrong album cover" symptom in DIMM-420 #1 is downstream of this
// same bug: when tmux eats the new transmit, the previous image at the
// same ID stays cached in the terminal.
func TestEncode_insideTmux_wrapsForPassthrough(t *testing.T) {
	t.Setenv("TERM", "tmux-256color")

	imgBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x01}
	out := Encode(imgBytes, 4, 2, 1)

	if !strings.Contains(out, "\x1bPtmux;") {
		t.Errorf("inside tmux, Encode output missing \\x1bPtmux; wrapper\noutput prefix: %q", truncForTest(out, 200))
	}
}
