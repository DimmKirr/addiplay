package fanart

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// Capabilities is the result of probing the current terminal stack
// for what addiplay can actually use. Populate via Probe() once at
// boot; callers consult it instead of guessing from env vars.
type Capabilities struct {
	// KittyGraphics — terminal responded OK to the Kitty graphics
	// protocol query (i.e. we can transmit raster images and have
	// them render).
	KittyGraphics bool

	// Truecolor — 24-bit ANSI color reaches the host terminal, so
	// the ASCII half-block fallback can render thumbnails as colored
	// glyphs.
	Truecolor bool

	// InTmux — we're running inside a tmux/screen multiplexer.
	InTmux bool

	// TmuxPassthrough — when InTmux, this records whether tmux has
	// `allow-passthrough on` set. When false, our `\x1bPtmux;…\x1b\\`
	// wrapper still gets eaten and Kitty graphics won't work.
	TmuxPassthrough bool

	// ProbeError holds whatever went wrong during probing, if
	// anything. A non-nil error doesn't mean Capabilities is
	// useless — individual flags may still be populated from env.
	ProbeError error

	// ProbeRan is true when an active probe (graphics query, tmux
	// shell-out) actually executed. False means we returned a
	// best-effort env-only Capabilities — useful in --doctor to
	// explain why the result might be optimistic/pessimistic.
	ProbeRan bool
}

// probeOnce / probedCaps cache the result so multiple DetectMode
// callers don't re-probe per render. Tests can reset via
// resetProbeForTests().
var (
	probeOnce  sync.Once
	probedCaps Capabilities
)

// Probe runs the full capability probe once per process and returns
// the cached result on subsequent calls. Safe to call from any
// goroutine; the underlying probe runs exactly once.
//
// Callers that want fresh probes (tests) can call resetProbeForTests().
func Probe() Capabilities {
	probeOnce.Do(func() {
		probedCaps = runProbe()
	})
	return probedCaps
}

func resetProbeForTests() {
	probeOnce = sync.Once{}
	probedCaps = Capabilities{}
}

// runProbe is the actual probe — split out so Probe() can wrap it in
// sync.Once. Touches /dev/tty (graphics query) and shells out to
// tmux. Best-effort: any individual failure leaves the corresponding
// field at its zero value and is recorded in ProbeError.
func runProbe() Capabilities {
	caps := Capabilities{
		InTmux:    inTmux(),
		Truecolor: hasTruecolorPassthrough(),
	}

	if caps.InTmux {
		ok, err := tmuxAllowPassthrough()
		caps.TmuxPassthrough = ok
		if err != nil && caps.ProbeError == nil {
			caps.ProbeError = err
		}
	}

	// Graphics probe — only if we have a real controlling terminal.
	// Non-tty (CI, piped stdout) short-circuits to env-derived guess.
	tty, ttyErr := openControllingTTY()
	if ttyErr != nil {
		if caps.ProbeError == nil {
			caps.ProbeError = ttyErr
		}
		caps.KittyGraphics = hostKittyCapable() // best-effort env guess
		return caps
	}
	defer func() { _ = tty.Close() }()

	fd := int(tty.Fd())
	prev, err := term.MakeRaw(fd)
	if err != nil {
		if caps.ProbeError == nil {
			caps.ProbeError = fmt.Errorf("raw mode: %w", err)
		}
		caps.KittyGraphics = hostKittyCapable()
		return caps
	}
	defer func() { _ = term.Restore(fd, prev) }()

	// Wrap the query in tmux passthrough when inside tmux, so the
	// query reaches the host terminal. Inside tmux without
	// passthrough, the wrap doesn't help — but the probe will time
	// out and report "no graphics", which is correct: we couldn't
	// emit graphics either.
	const probeID = 31
	query := fmt.Sprintf("\x1b_Gi=%d,a=q,t=d,f=24,s=1,v=1;AAAA\x1b\\\x1b[c", probeID)
	if caps.InTmux {
		query = tmuxWrapRaw(query)
	}
	_, _ = io.WriteString(tty, query)

	ok, err := readGraphicsResponse(tty, probeID, 200*time.Millisecond)
	if err != nil && caps.ProbeError == nil {
		caps.ProbeError = err
	}
	caps.KittyGraphics = ok
	caps.ProbeRan = true
	return caps
}

// openControllingTTY returns the user's terminal device for read+write,
// regardless of stdin/stdout redirection. Falls back to an error
// (caller must skip the active probe) when there's no terminal.
func openControllingTTY() (*os.File, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/tty: %w", err)
	}
	if !term.IsTerminal(int(tty.Fd())) {
		_ = tty.Close()
		return nil, fmt.Errorf("/dev/tty is not a terminal")
	}
	return tty, nil
}

// readGraphicsResponse reads bytes from the terminal until it sees a
// Primary DA reply (always terminated with 'c') or the deadline
// fires. Returns whether the graphics protocol was acknowledged.
func readGraphicsResponse(r io.Reader, id int, deadline time.Duration) (bool, error) {
	type result struct {
		buf []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var acc []byte
		tmp := make([]byte, 256)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				acc = append(acc, tmp[:n]...)
				if strings.Contains(string(acc), "\x1b[?") &&
					strings.HasSuffix(string(acc), "c") {
					ch <- result{buf: acc}
					return
				}
			}
			if err != nil {
				ch <- result{buf: acc, err: err}
				return
			}
		}
	}()
	select {
	case res := <-ch:
		return parseGraphicsResponse(res.buf, id), nil
	case <-time.After(deadline):
		return false, nil
	}
}

// tmuxWrapRaw is tmuxWrap without the inTmux() guard — used when the
// caller already knows it's inside tmux and wants the wrap
// unconditionally (e.g. the probe builds its query knowing it'll go
// through tmux).
func tmuxWrapRaw(esc string) string {
	doubled := strings.ReplaceAll(esc, "\x1b", "\x1b\x1b")
	return "\x1bPtmux;" + doubled + "\x1b\\"
}

// execFunc is the seam tmuxAllowPassthrough uses to call out to tmux.
// Tests inject a fake; production uses execCommandOutput.
type execFunc func(name string, args ...string) ([]byte, error)

func execCommandOutput(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// tmuxAllowPassthrough returns whether tmux's `allow-passthrough`
// option is on for the current session. Short-circuits to (false, nil)
// when $TMUX is unset (we're not inside tmux, so the question is
// moot). On exec error returns (false, err) — the caller decides
// whether to surface or ignore.
func tmuxAllowPassthrough() (bool, error) {
	return tmuxAllowPassthroughWith(execCommandOutput)
}

func tmuxAllowPassthroughWith(run execFunc) (bool, error) {
	if os.Getenv("TMUX") == "" {
		return false, nil
	}
	out, err := run("tmux", "show-options", "-gv", "allow-passthrough")
	if err != nil {
		return false, fmt.Errorf("tmux show-options: %w", err)
	}
	return strings.TrimSpace(string(out)) == "on", nil
}

// probeGraphicsCapability writes a Kitty graphics-protocol query
// followed by a Primary Device Attributes request to `rw` and waits
// up to `deadline` for the response. Returns true iff the response
// contains a `\x1b_Gi=<id>;` echo (Kitty's acknowledgement, even an
// error one).
//
// The DA tail-call is the trick that makes this robust on non-Kitty
// terminals: every conforming terminal replies to `\x1b[c`, so we
// always get SOMETHING back. The probe finishes the moment DA arrives
// (it's emitted strictly after the graphics ack), so we don't have to
// burn the full deadline on every non-supporting terminal.
//
// `deadline` should be modest (100-300ms). A non-tty or
// non-responsive terminal returns (false, nil) — silence is treated
// as "no support", not an error, because that's exactly how a vanilla
// xterm would behave if it didn't bother answering DA either.
func probeGraphicsCapability(rw io.ReadWriter, id int, deadline time.Duration) (bool, error) {
	// The query: a tiny 1×1 RGB pixel with a=q (query, don't store).
	// AAAA is base64 for three zero bytes (one black RGB pixel).
	query := fmt.Sprintf("\x1b_Gi=%d,a=q,t=d,f=24,s=1,v=1;AAAA\x1b\\\x1b[c", id)
	if _, err := io.WriteString(rw, query); err != nil {
		return false, fmt.Errorf("probe write: %w", err)
	}

	// Read into a small buffer with a deadline. We don't know how
	// many bytes are coming; loop until either we see a DA reply (the
	// terminator) or the deadline fires.
	type readResult struct {
		buf []byte
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		var acc []byte
		tmp := make([]byte, 256)
		for {
			n, err := rw.Read(tmp)
			if n > 0 {
				acc = append(acc, tmp[:n]...)
				// DA reply ends in 'c'. Once we have one, we're done.
				if strings.Contains(string(acc), "\x1b[?") &&
					strings.HasSuffix(string(acc), "c") {
					ch <- readResult{buf: acc}
					return
				}
			}
			if err != nil {
				ch <- readResult{buf: acc, err: err}
				return
			}
		}
	}()

	select {
	case res := <-ch:
		return parseGraphicsResponse(res.buf, id), nil
	case <-time.After(deadline):
		return false, nil // silence == no support
	}
}

// parseGraphicsResponse scans the bytes a terminal echoed back after
// our query for an `_Gi=<id>;...\x1b\\` reply. Any acknowledgement
// (OK or an explicit error code) means the terminal understood the
// protocol, which is what we care about. The Primary DA reply that
// follows is ignored — it's just there to guarantee SOME response
// from terminals that don't speak graphics at all.
func parseGraphicsResponse(b []byte, id int) bool {
	needle := fmt.Sprintf("\x1b_Gi=%d;", id)
	return strings.Contains(string(b), needle)
}
