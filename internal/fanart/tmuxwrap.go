package fanart

import "strings"

// tmuxWrap wraps a single terminal escape sequence in tmux's
// pass-through DCS form so it survives transit when stdout is going
// through tmux. Returns the input unchanged when not inside tmux.
//
// Why this exists: Kitty graphics-protocol escapes are APC sequences
// (`\x1b_G...\x1b\\`). tmux's parser doesn't recognise them as
// "passthrough material" by default — it sees an unknown APC and
// silently swallows it. tmux's documented escape hatch is to wrap the
// payload in `\x1bPtmux;...\x1b\\`, doubling any inner ESC bytes so
// the tmux parser doesn't terminate the wrapper early on the inner
// `\x1b\\` of the wrapped sequence. The user must also have
// `set -g allow-passthrough on` in tmux ≥ 3.3 — addiplay can't enforce
// that part, but it can at least produce the correct wire form.
//
// Spec: tmux(1), "ALLOW-PASSTHROUGH" section.
func tmuxWrap(esc string) string {
	if !inTmux() {
		return esc
	}
	// Double every inner ESC. strings.ReplaceAll handles the byte-level
	// substitution cleanly; nothing else in our escape uses 0x1b for
	// non-control purposes so no false positives.
	doubled := strings.ReplaceAll(esc, "\x1b", "\x1b\x1b")
	return "\x1bPtmux;" + doubled + "\x1b\\"
}
