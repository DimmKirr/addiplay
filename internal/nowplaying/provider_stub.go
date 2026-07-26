//go:build !darwin && !linux

package nowplaying

import "os"

// New returns the logging fallback on unsupported platforms.
func New(handler Handler, opts ...Option) Provider {
	cfg := options(opts)
	w := cfg.logWriter
	if w == nil {
		w = os.Stderr
	}
	return NewLog(w)
}
