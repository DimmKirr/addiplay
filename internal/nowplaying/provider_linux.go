//go:build linux

package nowplaying

import "io"

// New returns the Linux provider backed by MPRIS over D-Bus.
//
// TODO(DIMM-424): implement MPRIS via go-mpris-server. Until then, fall
// back to the logging provider so the package compiles and the
// integration path is exercisable.
func New(handler Handler, opts ...Option) Provider {
	cfg := options(opts)
	w := cfg.logWriter
	if w == nil {
		w = io.Discard
	}
	return NewLog(w)
}
