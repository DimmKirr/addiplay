package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// debugSink bundles the per-session debug-log handles used by `play` and
// `tui` commands when --debug is set.
type debugSink struct {
	// Writer is for addiplay's own log messages (timestamped, prefixed).
	Writer io.Writer
	// MPVLogPath is the file path mpv writes its --log-file output to. May
	// equal the addiplay log path (interleaved) or be a separate file.
	MPVLogPath string
}

// openDebugLog opens (or creates+truncates) the --debug-log path when the
// --debug flag is set. Returns (nil, nil) when debug is off. The closer
// writes a "session ended" footer.
//
// We truncate the file once at startup, write our header, then return a
// debugWriter that appends. mpv is given the same path via --log-file and
// opens it in O_APPEND mode, so mpv's verbose output interleaves with
// addiplay's own events in a single file the user can `tail -f`.
func openDebugLog() (*debugSink, func(), error) {
	if !debug {
		return nil, func() {}, nil
	}
	// Resolve absolute path so mpv (which has a different cwd) writes to
	// the right file.
	mpvAbs, err := filepath.Abs(debugLog)
	if err != nil {
		return nil, func() {}, fmt.Errorf("resolve debug log path: %w", err)
	}
	// addiplay's own events go to <debug.log>.addiplay rather than
	// sharing the file with mpv. Previously we shared, but mpv truncates
	// its --log-file on open and wipes out every addiplay write from
	// before the player launched (NewModel, runTUI startup, the first
	// channelsLoaded, key presses leading up to player init). Two files
	// is the only reliable way to keep both halves of the trace.
	addiAbs := mpvAbs + ".addiplay"
	f, err := os.OpenFile(addiAbs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open debug log %s: %w", addiAbs, err)
	}
	w := &debugWriter{w: f}
	w.line("=== addiplay debug log @ %s — version %s ===", time.Now().Format(time.RFC3339), Version)
	w.line("=== mpv output goes to %s (--log-file) ===", mpvAbs)
	w.line("=== addiplay events go to %s (this file) ===", addiAbs)
	return &debugSink{Writer: w, MPVLogPath: mpvAbs}, func() {
		w.line("=== addiplay session ended @ %s ===", time.Now().Format(time.RFC3339))
		_ = f.Close()
	}, nil
}

// debugWriter prefixes each newline-terminated chunk with a timestamp so
// mpv output and our own events interleave readably.
type debugWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (d *debugWriter) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ts := time.Now().Format("15:04:05.000")
	// Don't try to be clever about line splitting — mpv buffers its stderr;
	// just prefix each Write call so chronology is preserved.
	if _, err := fmt.Fprintf(d.w, "[%s] ", ts); err != nil {
		return 0, err
	}
	return d.w.Write(p)
}

// line is a convenience for addiplay-emitted (not mpv) events.
func (d *debugWriter) line(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ts := time.Now().Format("15:04:05.000")
	_, _ = fmt.Fprintf(d.w, "[%s] addiplay: ", ts)
	_, _ = fmt.Fprintf(d.w, format, args...)
	_, _ = fmt.Fprintln(d.w)
}
