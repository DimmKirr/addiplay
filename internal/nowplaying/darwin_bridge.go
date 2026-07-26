//go:build darwin

package nowplaying

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework MediaPlayer -framework Foundation -framework AppKit
#include "darwin_bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unsafe"
)

//go:embed probe_nowplaying.sh
var probeScript []byte

// globalHandler stores the current Handler so the C → Go callbacks can
// reach it. Protected by globalMu. Only one darwinProvider can be active
// at a time (macOS has a single MPNowPlayingInfoCenter), so a package-
// level variable is appropriate.
var (
	globalMu      sync.Mutex
	globalHandler Handler
	globalLog     io.Writer
)

type darwinProvider struct {
	mu     sync.Mutex
	closed bool
	log    io.Writer
}

func newDarwin(handler Handler, opts ...Option) *darwinProvider {
	cfg := options(opts)
	w := cfg.logWriter
	if w == nil {
		w = io.Discard
	}

	globalMu.Lock()
	globalHandler = handler
	globalLog = w
	globalMu.Unlock()

	C.NowPlayingSetup()
	fmt.Fprintf(w, "[nowplaying] Init (darwin provider)\n")
	return &darwinProvider{log: w}
}

func (p *darwinProvider) Update(artist, title string, durationSec int, artURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	ca := C.CString(artist)
	ct := C.CString(title)
	curl := C.CString(artURL)
	C.NowPlayingUpdate(ca, ct, C.int(durationSec), curl)
	C.free(unsafe.Pointer(ca))
	C.free(unsafe.Pointer(ct))
	C.free(unsafe.Pointer(curl))
	fmt.Fprintf(p.log, "[nowplaying] Update artist=%q title=%q duration=%d artURL=%q\n", artist, title, durationSec, artURL)

	go p.runProbe()
}

func (p *darwinProvider) SetPlaying(playing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	v := C.int(0)
	if playing {
		v = 1
	}
	C.NowPlayingSetPlaybackState(v)
	fmt.Fprintf(p.log, "[nowplaying] SetPlaying playing=%v\n", playing)
}

func (p *darwinProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	C.NowPlayingTeardown()

	globalMu.Lock()
	globalHandler = nil
	globalLog = nil
	globalMu.Unlock()

	fmt.Fprintf(p.log, "[nowplaying] Close\n")
}

func (p *darwinProvider) runProbe() {
	// Give macOS a moment to propagate the Now Playing info.
	time.Sleep(500 * time.Millisecond)

	tmp, err := os.CreateTemp("", "addiplay-probe-*.sh")
	if err != nil {
		fmt.Fprintf(p.log, "[tmux-debug] failed to create probe script: %v\n", err)
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(probeScript); err != nil {
		tmp.Close()
		fmt.Fprintf(p.log, "[tmux-debug] failed to write probe script: %v\n", err)
		return
	}
	tmp.Close()
	_ = os.Chmod(tmp.Name(), 0755)

	cmd := exec.Command("bash", tmp.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(p.log, "[tmux-debug] probe exec error: %v\n", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fmt.Fprintf(p.log, "[tmux-debug] %s\n", line)
	}
}

//export goNowPlayingLog
func goNowPlayingLog(cmsg *C.char) {
	globalMu.Lock()
	w := globalLog
	globalMu.Unlock()
	if w != nil {
		fmt.Fprintf(w, "[nowplaying] [objc] %s\n", C.GoString(cmsg))
	}
}

//export goNowPlayingPlayPause
func goNowPlayingPlayPause() {
	globalMu.Lock()
	h := globalHandler
	w := globalLog
	globalMu.Unlock()
	if w != nil {
		fmt.Fprintf(w, "[nowplaying] MediaKey PlayPause\n")
	}
	if h != nil {
		h(CmdPlayPause)
	}
}

//export goNowPlayingNext
func goNowPlayingNext() {
	globalMu.Lock()
	h := globalHandler
	w := globalLog
	globalMu.Unlock()
	if w != nil {
		fmt.Fprintf(w, "[nowplaying] MediaKey Next\n")
	}
	if h != nil {
		h(CmdNext)
	}
}

//export goNowPlayingPrevious
func goNowPlayingPrevious() {
	globalMu.Lock()
	h := globalHandler
	w := globalLog
	globalMu.Unlock()
	if w != nil {
		fmt.Fprintf(w, "[nowplaying] MediaKey Previous\n")
	}
	if h != nil {
		h(CmdPrevious)
	}
}
