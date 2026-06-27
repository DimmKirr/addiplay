package ui

import (
	"fmt"
	"io"
	"sync"
)

// Package-level diagnostic sink. Mirrors the fanart package's pattern:
// default is io.Discard; cmd/tui.go wires it to the --debug-log file via
// SetDebugLogger when the user passes --debug.
//
// What gets logged: every UI lifecycle transition that's interesting for
// diagnosing hangs or unexpected behaviour — model construction, channel
// load, key handlers (especially quit), Cmd dispatch, and per-Kitty-image
// fetch/render events. Lines are short and timestamped (the debugWriter
// in cmd/debug.go prefixes the timestamp).
var (
	debugLogMu sync.Mutex
	debugLog   io.Writer = io.Discard
)

// SetDebugLogger installs a writer for ui diagnostics. Safe to call
// concurrently with any UI code. Pass io.Discard (or nil) to disable.
func SetDebugLogger(w io.Writer) {
	debugLogMu.Lock()
	defer debugLogMu.Unlock()
	if w == nil {
		w = io.Discard
	}
	debugLog = w
}

// dlog writes a timestamped diagnostic line. Cheap when debug is off
// (single mutex acquire + io.Discard.Write which is a no-op).
func dlog(format string, args ...any) {
	debugLogMu.Lock()
	w := debugLog
	debugLogMu.Unlock()
	_, _ = fmt.Fprintf(w, "[ui] "+format+"\n", args...)
}
