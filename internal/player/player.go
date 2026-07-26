// Package player wraps an mpv subprocess controlled over a JSON-IPC Unix
// socket. The TUI never calls mpv directly; it Plays/Pauses/Stops/Volumes
// through this thin facade and consumes State events.
package player

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dimmkirr/addiplay/internal/nowplaying"
)

//go:embed nowplaying.lua
var nowplayingScript []byte

// State enumerates the player lifecycle as seen by the UI.
type State int

const (
	StateIdle State = iota
	StateLoading
	StatePlaying
	StatePaused
	StateError
)

func (s State) String() string {
	return [...]string{"idle", "loading", "playing", "paused", "error"}[s]
}

// MediaCommand represents a media-key action received from the OS now-playing
// integration (macOS MPRemoteCommandCenter, Linux MPRIS).
type MediaCommand int

const (
	MediaCommandNone     MediaCommand = iota
	MediaCommandPlayPause
	MediaCommandNext
	MediaCommandPrevious
)

func (c MediaCommand) String() string {
	return [...]string{"None", "PlayPause", "Next", "Previous"}[c]
}

// Event is a state transition or an mpv-side message.
type Event struct {
	State           State
	URL             string       // last requested URL (may be empty)
	Err             error        // populated when State == StateError
	MediaTitle      string       // populated only on media-title property changes
	MetadataChanged bool         // true when stream metadata changed (ICY title update)
	MediaCommand    MediaCommand // populated when OS media key is pressed
}

// Player owns the mpv subprocess and IPC socket.
type Player struct {
	socketPath string
	cmd        *exec.Cmd
	stderr     *bytes.Buffer // captured mpv stderr — surfaced in startup errors
	conn       net.Conn
	enc        *json.Encoder
	dec        *json.Decoder

	events    chan Event
	state     atomic.Int32
	lastURL   atomic.Value // string
	requestID atomic.Int64
	closeOnce sync.Once
	closed    chan struct{}
	writeMu   sync.Mutex // serializes Encoder writes

	np nowplaying.Provider // OS now-playing integration (nil if not configured)
}

// Option configures a Player at construction time.
type Option func(*config)

type config struct {
	debugWriter io.Writer          // addiplay-side log destination (in-memory startup, optional tee)
	mpvLogPath  string             // file path passed to mpv's --log-file flag
	np          nowplaying.Provider // OS now-playing provider
}

// WithDebugWriter routes mpv's stdout/stderr to w in addition to the
// in-memory startup-error buffer. Pair with WithMPVLogFile for the verbose
// mpv log (mpv suppresses stdout/stderr when --no-terminal is set, so a
// dedicated log-file is the only way to capture its full output).
func WithDebugWriter(w io.Writer) Option {
	return func(c *config) { c.debugWriter = w }
}

// WithMPVLogFile tells mpv to write its full verbose log (--log-file plus
// --msg-level=all=v) directly to path. mpv opens the file in append mode,
// so callers that share a debug log with addiplay's own writes should write
// their header BEFORE the player starts and avoid concurrent writes.
func WithMPVLogFile(path string) Option {
	return func(c *config) { c.mpvLogPath = path }
}

// WithNowPlaying attaches an OS now-playing provider. The player delegates
// track metadata and playback state to it, and closes it on shutdown.
func WithNowPlaying(p nowplaying.Provider) Option {
	return func(c *config) { c.np = p }
}

// New starts an idle mpv subprocess and connects over Unix socket. The
// returned Player must be Close()d.
func New(ctx context.Context, opts ...Option) (*Player, error) {
	return newPlayer(ctx, "", opts...)
}

// NewWithSocket connects to an existing IPC socket without spawning mpv.
// Used by tests with the testutil mpv fake.
func NewWithSocket(ctx context.Context, socketPath string, opts ...Option) (*Player, error) {
	return newPlayer(ctx, socketPath, opts...)
}

func newPlayer(ctx context.Context, existingSocket string, opts ...Option) (*Player, error) {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	p := &Player{
		events: make(chan Event, 32),
		closed: make(chan struct{}),
		np:     cfg.np,
	}
	p.state.Store(int32(StateIdle))
	p.lastURL.Store("")

	if existingSocket == "" {
		if _, err := exec.LookPath("mpv"); err != nil {
			return nil, fmt.Errorf("mpv binary not found in PATH — install mpv (https://mpv.io/installation/) and try again")
		}
		reapOrphanedMPV()
		dir, err := os.MkdirTemp("", "addiplay-mpv-*")
		if err != nil {
			return nil, err
		}
		p.socketPath = filepath.Join(dir, "ipc.sock")
		scriptPath := filepath.Join(dir, "nowplaying.lua")
		if err := os.WriteFile(scriptPath, nowplayingScript, 0644); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("write nowplaying script: %w", err)
		}
		args := []string{
			"--idle=yes",
			"--no-video",
			"--no-terminal",
			"--no-input-terminal",
			"--input-ipc-server=" + p.socketPath,
			"--script=" + scriptPath,
		}
		if cfg.np != nil {
			args = append(args, "--input-media-keys=no")
		}
		if cfg.mpvLogPath != "" {
			// --log-file is the ONLY way to get mpv output while
			// --no-terminal is in effect (which it must be — without it,
			// mpv steals stdin/stdout from our TUI). It writes the same
			// content stdout/stderr would have, plus all verbose logs.
			args = append(args,
				"--log-file="+cfg.mpvLogPath,
				"--msg-level=all=v",
			)
		}
		p.cmd = exec.CommandContext(ctx, "mpv", args...)
		p.stderr = &bytes.Buffer{}
		if cfg.debugWriter != nil {
			// Tee mpv stderr to (1) in-memory buffer for startup error
			// reporting and (2) caller-supplied debug log. Also tee mpv
			// stdout to the debug log — mpv-with-`--no-terminal` rarely
			// writes there, but anything it does emit is diagnostic gold.
			// Tag each stream so they're distinguishable when interleaved.
			p.cmd.Stderr = io.MultiWriter(p.stderr, &taggedWriter{tag: "mpv-stderr", w: cfg.debugWriter})
			p.cmd.Stdout = &taggedWriter{tag: "mpv-stdout", w: cfg.debugWriter}
		} else {
			p.cmd.Stderr = p.stderr
		}
		if err := p.cmd.Start(); err != nil {
			return nil, fmt.Errorf("start mpv: %w", err)
		}
		// Reap the process so cmd.ProcessState populates when mpv exits.
		// Without this, waitForSocketOrExit can't detect early exits.
		go func() { _ = p.cmd.Wait() }()
	} else {
		p.socketPath = existingSocket
	}

	// Wait for socket to appear, or for mpv to exit early. Either way,
	// surface anything mpv said on stderr so the user can diagnose.
	if err := waitForSocketOrExit(p, 5*time.Second); err != nil {
		_ = p.killProcess()
		return nil, err
	}

	conn, err := net.Dial("unix", p.socketPath)
	if err != nil {
		_ = p.killProcess()
		return nil, fmt.Errorf("dial mpv socket: %w", err)
	}
	p.conn = conn
	p.enc = json.NewEncoder(conn)
	p.dec = json.NewDecoder(bufio.NewReader(conn))

	go p.readLoop()

	// Observe the "pause" property so we detect external pause/unpause
	// (macOS media keys, AirPods, Control Center). mpv ≥0.21 deprecated the
	// "pause"/"unpause" events — property observation is the only way.
	_ = p.send([]any{"observe_property", 1, "pause"})
	_ = p.send([]any{"observe_property", 2, "media-title"})
	_ = p.send([]any{"observe_property", 3, "metadata"})

	return p, nil
}

// Events returns the event stream. Drained by the UI's Bubble Tea loop.
func (p *Player) Events() <-chan Event { return p.events }

// InjectMediaCommand pushes a media-key event into the event stream so the
// TUI handles it like any other player event. Called from the nowplaying
// handler callback (OS media keys → CGo → this method → Bubble Tea).
func (p *Player) InjectMediaCommand(cmd MediaCommand) {
	select {
	case p.events <- Event{MediaCommand: cmd}:
	default:
	}
}

// State returns the current state snapshot.
func (p *Player) State() State { return State(p.state.Load()) }

// Play replaces the current stream and starts playing.
func (p *Player) Play(url string) error {
	p.lastURL.Store(url)
	p.setState(StateLoading, url, nil)
	if err := p.send([]any{"loadfile", url, "replace"}); err != nil {
		return err
	}
	if p.np != nil {
		p.np.SetPlaying(true)
	}
	// Reset pause — loadfile preserves mpv's pause state, so if something
	// external paused us (macOS media keys, AirPods) the new track would
	// start paused with no audio output.
	return p.send([]any{"set_property", "pause", false})
}

// Pause pauses playback if playing.
func (p *Player) Pause() error {
	if err := p.send([]any{"set_property", "pause", true}); err != nil {
		return err
	}
	if p.np != nil {
		p.np.SetPlaying(false)
	}
	p.setState(StatePaused, "", nil)
	return nil
}

// Resume resumes playback if paused.
func (p *Player) Resume() error {
	if err := p.send([]any{"set_property", "pause", false}); err != nil {
		return err
	}
	if p.np != nil {
		p.np.SetPlaying(true)
	}
	p.setState(StatePlaying, "", nil)
	return nil
}

// Stop halts playback and returns mpv to idle.
func (p *Player) Stop() error {
	if err := p.send([]any{"stop"}); err != nil {
		return err
	}
	if p.np != nil {
		p.np.SetPlaying(false)
	}
	p.setState(StateIdle, "", nil)
	return nil
}

// SetTrackMetadata sets the currently-playing track's artist and title. When
// a nowplaying.Provider is attached (WithNowPlaying), metadata is pushed
// directly to the OS (separate artist/title fields). Otherwise falls back to
// the mpv Lua script which combines them into force-media-title.
func (p *Player) SetTrackMetadata(artist, title string, durationSec int, artURL string) error {
	if p.np != nil {
		p.np.Update(artist, title, durationSec, artURL)
		return nil
	}
	return p.send([]any{"script-message", "set-track-metadata", artist, title})
}

// SetVolume sets mpv's volume (0..100).
func (p *Player) SetVolume(pct int) error {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return p.send([]any{"set_property", "volume", pct})
}

// Close stops mpv and tears the socket down.
func (p *Player) Close() error {
	var firstErr error
	p.closeOnce.Do(func() {
		close(p.closed)
		if p.np != nil {
			p.np.Close()
		}
		if p.conn != nil {
			_ = p.conn.Close()
		}
		firstErr = p.killProcess()
	})
	return firstErr
}

// -----------------------------------------------------------------------------
// internals
// -----------------------------------------------------------------------------

func (p *Player) send(command []any) error {
	if p.enc == nil {
		return errors.New("player: not connected")
	}
	id := p.requestID.Add(1)
	msg := map[string]any{
		"command":    command,
		"request_id": id,
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.enc.Encode(msg)
}

func (p *Player) readLoop() {
	for {
		select {
		case <-p.closed:
			return
		default:
		}
		var msg map[string]any
		if err := p.dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				p.setState(StateIdle, "", nil)
				return
			}
			p.setState(StateError, "", fmt.Errorf("read mpv: %w", err))
			return
		}
		p.handleMessage(msg)
	}
}

func (p *Player) handleMessage(msg map[string]any) {
	event, _ := msg["event"].(string)
	switch event {
	case "start-file":
		p.setState(StateLoading, "", nil)
	case "playback-restart", "file-loaded":
		p.setState(StatePlaying, "", nil)
	case "pause":
		p.setState(StatePaused, "", nil)
	case "unpause":
		p.setState(StatePlaying, "", nil)
	case "property-change":
		name, _ := msg["name"].(string)
		switch name {
		case "pause":
			if paused, ok := msg["data"].(bool); ok {
				if paused {
					p.setState(StatePaused, "", nil)
				} else if p.State() == StatePaused {
					p.setState(StatePlaying, "", nil)
				}
			}
		case "media-title":
			if title, ok := msg["data"].(string); ok && title != "" {
				p.emitMediaTitle(title)
			}
		case "metadata":
			p.emitMetadataChanged()
		}
	case "end-file":
		// "reason" can be "error" or "eof"
		if r, _ := msg["reason"].(string); r == "error" {
			p.setState(StateError, "", fmt.Errorf("mpv: stream error"))
		} else if p.State() != StateLoading {
			// Skip the idle transition if we're already loading the next
			// track — end-file for the outgoing track arrives after the
			// new loadfile command, and going idle here would trigger a
			// spurious auto-advance.
			p.setState(StateIdle, "", nil)
		}
	case "idle":
		// only reset to Idle if we weren't mid-load
		if p.State() != StateLoading {
			p.setState(StateIdle, "", nil)
		}
	}
}

func (p *Player) emitMediaTitle(title string) {
	lastURL, _ := p.lastURL.Load().(string)
	select {
	case p.events <- Event{State: State(p.state.Load()), URL: lastURL, MediaTitle: title}:
	default:
	}
}

func (p *Player) emitMetadataChanged() {
	lastURL, _ := p.lastURL.Load().(string)
	select {
	case p.events <- Event{State: State(p.state.Load()), URL: lastURL, MetadataChanged: true}:
	default:
	}
}

func (p *Player) setState(s State, url string, err error) {
	prev := State(p.state.Swap(int32(s)))
	if prev == s && err == nil {
		return
	}
	lastURL, _ := p.lastURL.Load().(string)
	if url != "" {
		lastURL = url
	}
	select {
	case p.events <- Event{State: s, URL: lastURL, Err: err}:
	default:
		// drop if UI isn't draining; state can still be queried via State().
	}
}

func (p *Player) killProcess() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Kill()
	// DO NOT call p.cmd.Wait() here. The reaper goroutine spawned in
	// New() (`go func() { _ = p.cmd.Wait() }()`) is already inside
	// Wait(). Per os/exec docs: "Wait must not be called concurrently
	// with itself" — the kernel delivers the exit status to exactly
	// one of the two Wait() calls and the other blocks FOREVER. This
	// was the root cause of the user-reported "hangs on quit": the
	// quit handler called m.player.Close() → killProcess() → second
	// Wait() → deadlock, even though SIGKILL had already terminated
	// mpv. The reaper goroutine handles process reaping; we just need
	// to send the kill signal and clean up the socket file.
	if p.socketPath != "" {
		_ = os.RemoveAll(filepath.Dir(p.socketPath))
	}
	return nil
}

// reapOrphanedMPV kills any mpv processes left behind by a previous
// addiplay that crashed without cleaning up. Matches on the
// --input-ipc-server=/tmp/addiplay-mpv-* argument pattern.
func reapOrphanedMPV() {
	out, err := exec.Command("pgrep", "-f", "mpv.*addiplay-mpv").Output()
	if err != nil || len(out) == 0 {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		_ = exec.Command("kill", "-9", line).Run()
	}
	// Clean up stale socket dirs from crashed sessions.
	matches, _ := filepath.Glob(os.TempDir() + "/addiplay-mpv-*")
	for _, d := range matches {
		_ = os.RemoveAll(d)
	}
}

// waitForSocketOrExit polls for the IPC socket file to appear. If mpv
// exits before the socket shows up (the common failure mode), the error
// includes the captured stderr so the user can see why.
func waitForSocketOrExit(p *Player, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p.socketPath); err == nil {
			return nil
		}
		// Did mpv exit early?
		if p.cmd != nil && p.cmd.ProcessState != nil {
			return mpvStartupError(p, "mpv exited before opening its IPC socket")
		}
		// Non-blocking check: poll Process.Signal(0)-style by inspecting ProcessState
		// after a tiny wait. exec.Cmd doesn't expose a non-blocking Wait; the
		// canonical workaround is to call Wait in a goroutine. Cheaper: just keep
		// looping — when mpv dies, ProcessState gets populated via cmd.Wait or
		// when the OS reaps it eventually. So we ALSO try Wait in a goroutine:
		time.Sleep(30 * time.Millisecond)
	}
	return mpvStartupError(p, fmt.Sprintf("timed out after %s waiting for mpv IPC socket %s", timeout, p.socketPath))
}

// taggedWriter prefixes each Write with "[tag] " so when mpv stderr and
// mpv stdout share the same debug log they're distinguishable.
type taggedWriter struct {
	tag string
	w   io.Writer
}

func (t *taggedWriter) Write(p []byte) (int, error) {
	if _, err := fmt.Fprintf(t.w, "[%s] ", t.tag); err != nil {
		return 0, err
	}
	return t.w.Write(p)
}

// mpvStartupError wraps a startup failure with mpv's stderr (if any) so the
// user has a hope of diagnosing it.
func mpvStartupError(p *Player, base string) error {
	stderr := ""
	if p.stderr != nil {
		stderr = strings.TrimSpace(p.stderr.String())
	}
	if stderr == "" {
		return fmt.Errorf(`%s

Try running this manually to see what mpv reports:

    mpv --idle=yes --no-video --no-terminal --input-ipc-server=/tmp/addiplay-test.sock

Common causes:
  - broken ~/.config/mpv/mpv.conf (test with: mpv --no-config …)
  - mpv too old (< 0.7); check: mpv --version
  - audio output init failure on macOS (rare with --no-video --no-terminal)`, base)
	}
	return fmt.Errorf("%s\n\nmpv stderr:\n%s", base, stderr)
}
