package fanart

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestTmuxAllowPassthrough_returnsTrueWhenTmuxSaysOn — the happy
// path: $TMUX is set, `tmux show-options -gv allow-passthrough`
// prints "on", probe returns true.
func TestTmuxAllowPassthrough_returnsTrueWhenTmuxSaysOn(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")
	got, err := tmuxAllowPassthroughWith(func(name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			t.Errorf("unexpected exec: %s %v", name, args)
		}
		return []byte("on\n"), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got {
		t.Error("got=false, want true")
	}
}

// TestTmuxAllowPassthrough_returnsFalseWhenTmuxSaysOff — passthrough
// explicitly off in tmux config.
func TestTmuxAllowPassthrough_returnsFalseWhenTmuxSaysOff(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")
	got, err := tmuxAllowPassthroughWith(func(_ string, _ ...string) ([]byte, error) {
		return []byte("off\n"), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got {
		t.Error("got=true, want false")
	}
}

// TestTmuxAllowPassthrough_returnsFalseWhenNotInTmux — no $TMUX set
// means we're not under tmux; don't shell out, return false.
func TestTmuxAllowPassthrough_returnsFalseWhenNotInTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	called := false
	got, err := tmuxAllowPassthroughWith(func(_ string, _ ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got {
		t.Error("got=true, want false (no TMUX)")
	}
	if called {
		t.Error("shell-out happened when not in tmux — must short-circuit")
	}
}

// TestTmuxAllowPassthrough_handlesTmuxExecError — tmux binary
// missing or command fails; treat as "we can't tell, assume off"
// so we don't lie about capabilities.
func TestTmuxAllowPassthrough_handlesTmuxExecError(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")
	got, err := tmuxAllowPassthroughWith(func(_ string, _ ...string) ([]byte, error) {
		return nil, errors.New("tmux: command not found")
	})
	if err == nil {
		t.Error("expected err for tmux exec failure, got nil")
	}
	if got {
		t.Error("got=true on error, want false")
	}
}

// ---- Active graphics probe -----------------------------------------------

// TestParseGraphicsResponse_recognisesOK — when the terminal sends
// back the canonical `\x1b_Gi=<id>;OK\x1b\\` reply, we conclude
// graphics support.
func TestParseGraphicsResponse_recognisesOK(t *testing.T) {
	// Real Kitty response: graphics ACK + Primary DA.
	resp := []byte("\x1b_Gi=31;OK\x1b\\\x1b[?64;1;2;6;9;15;22c")
	if !parseGraphicsResponse(resp, 31) {
		t.Errorf("did not recognise OK in %q", resp)
	}
}

// TestParseGraphicsResponse_rejectsDAOnly — terminal without
// graphics support just emits the DA reply; no graphics.
func TestParseGraphicsResponse_rejectsDAOnly(t *testing.T) {
	resp := []byte("\x1b[?1;2c") // xterm's DA, no Kitty response
	if parseGraphicsResponse(resp, 31) {
		t.Errorf("falsely recognised graphics in DA-only response %q", resp)
	}
}

// TestParseGraphicsResponse_rejectsWrongID — defensive: someone
// else's stray response with a different id must not count.
func TestParseGraphicsResponse_rejectsWrongID(t *testing.T) {
	resp := []byte("\x1b_Gi=99;OK\x1b\\")
	if parseGraphicsResponse(resp, 31) {
		t.Errorf("falsely matched on wrong id in %q", resp)
	}
}

// TestParseGraphicsResponse_recognisesError — Kitty does respond
// to malformed queries with `_Gi=<id>;ERROR:...\x1b\\`. That's
// still "graphics protocol understood", so we treat as supported.
func TestParseGraphicsResponse_recognisesError(t *testing.T) {
	resp := []byte("\x1b_Gi=31;ENOENT:bad\x1b\\")
	if !parseGraphicsResponse(resp, 31) {
		t.Errorf("expected to recognise i=31 echo as supported in %q", resp)
	}
}

// fakeTTY is a tiny in-memory ReadWriter for probe tests. Writes go
// to /dev/null; reads stream a canned response (split into chunks
// optionally with a small delay between chunks to simulate a real
// terminal trickling bytes back).
type fakeTTY struct {
	chunks [][]byte
	delay  time.Duration
	r      *io.PipeReader
	w      *io.PipeWriter
}

func newFakeTTY(response []byte, delay time.Duration) *fakeTTY {
	r, w := io.Pipe()
	tt := &fakeTTY{chunks: [][]byte{response}, delay: delay, r: r, w: w}
	go tt.pump()
	return tt
}

func newFakeTTYTimeout() *fakeTTY {
	// Pipe writer never writes — read will block until the test
	// deadline fires.
	r, w := io.Pipe()
	return &fakeTTY{r: r, w: w}
}

func (t *fakeTTY) pump() {
	for _, c := range t.chunks {
		if t.delay > 0 {
			time.Sleep(t.delay)
		}
		_, _ = t.w.Write(c)
	}
	// Don't close — let the probe time out naturally if it's reading
	// for more.
}

func (t *fakeTTY) Read(p []byte) (int, error)  { return t.r.Read(p) }
func (t *fakeTTY) Write(p []byte) (int, error) { return io.Discard.Write(p) }
func (t *fakeTTY) Close() error                { _ = t.r.Close(); return t.w.Close() }

// TestProbeGraphicsCapability_recognisesKittyOK — terminal sends back
// `\x1b_Gi=N;OK\x1b\\` after our query → probe returns true.
func TestProbeGraphicsCapability_recognisesKittyOK(t *testing.T) {
	tt := newFakeTTY([]byte("\x1b_Gi=31;OK\x1b\\\x1b[?64;1c"), 0)
	defer func() { _ = tt.Close() }()

	got, err := probeGraphicsCapability(tt, 31, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got {
		t.Error("got=false, want true")
	}
}

// TestProbeGraphicsCapability_rejectsDAOnly — terminal that doesn't
// speak Kitty responds only to the Primary DA part of our query.
func TestProbeGraphicsCapability_rejectsDAOnly(t *testing.T) {
	tt := newFakeTTY([]byte("\x1b[?1;2c"), 0)
	defer func() { _ = tt.Close() }()

	got, err := probeGraphicsCapability(tt, 31, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got {
		t.Error("got=true, want false (no Kitty echo)")
	}
}

// TestProbeGraphicsCapability_timesOutSilently — some terminals
// never respond (locked-down ssh, kiosk, etc.). Probe must finish
// within the deadline, returning false (with no error — silence is
// not an error here, it's a useful negative signal).
func TestProbeGraphicsCapability_timesOutSilently(t *testing.T) {
	tt := newFakeTTYTimeout()
	defer func() { _ = tt.Close() }()

	start := time.Now()
	got, err := probeGraphicsCapability(tt, 31, 80*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("timeout should not be an error; got %v", err)
	}
	if got {
		t.Error("got=true on timeout, want false")
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("probe took %v, expected ≈ deadline (80ms)", elapsed)
	}
}

// TestProbeGraphicsCapability_emitsCorrectQuery — black-box assertion
// on the bytes we send the terminal. Wraps the writer side in a
// buffer to capture, mirrors the response side from a pipe.
func TestProbeGraphicsCapability_emitsCorrectQuery(t *testing.T) {
	var captured bytes.Buffer
	r, w := io.Pipe()
	go func() {
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("\x1b[?1;2c")) // unblock the read
	}()
	rw := struct {
		io.Reader
		io.Writer
	}{Reader: r, Writer: &captured}

	_, _ = probeGraphicsCapability(rw, 31, 200*time.Millisecond)
	q := captured.String()
	// Must contain: a Kitty graphics query for id=31 and a Primary DA.
	if !strings.Contains(q, "\x1b_Gi=31,a=q") {
		t.Errorf("query missing `\\x1b_Gi=31,a=q`; got %q", q)
	}
	if !strings.Contains(q, "\x1b[c") {
		t.Errorf("query missing Primary DA `\\x1b[c`; got %q", q)
	}
}

// suppress unused import warning when these tests are excluded
var _ = errors.New

