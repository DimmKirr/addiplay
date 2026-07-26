// Logging fallback provider — used on unsupported platforms and in tests.
// Every call is logged so the integration flow is observable without an OS
// media framework.

package nowplaying

import (
	"fmt"
	"io"
	"sync"
)

type logProvider struct {
	w       io.Writer
	mu      sync.Mutex
	closed  bool
}

func (p *logProvider) Update(artist, title string, durationSec int, artURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	fmt.Fprintf(p.w, "[nowplaying] Update artist=%q title=%q duration=%d artURL=%q\n", artist, title, durationSec, artURL)
}

func (p *logProvider) SetPlaying(playing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	fmt.Fprintf(p.w, "[nowplaying] SetPlaying playing=%v\n", playing)
}

func (p *logProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	fmt.Fprintf(p.w, "[nowplaying] Close\n")
}

// NewLog returns a Provider that logs every call to w. Useful for tests,
// CI, and platforms without a native media framework.
func NewLog(w io.Writer) Provider {
	fmt.Fprintf(w, "[nowplaying] Init (log provider)\n")
	return &logProvider{w: w}
}
