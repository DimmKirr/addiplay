// Package demo provides fake implementations of the audioaddict client and
// player so the TUI can run without real credentials, network, or mpv.
//
// The fixtures (under fixtures/*.json) are embedded into the binary via
// //go:embed so `addiplay demo` works on any user's machine offline.
// Tests can import this package to get deterministic data without
// duplicating fixtures under each package's testdata/.
package demo

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/player"
)

//go:embed fixtures/*.json
var fixturesFS embed.FS

// Creds returns a synthetic logged-in identity good enough for the TUI to
// boot without hitting the real keyring.
func Creds() creds.Creds {
	return creds.Creds{Email: "demo@addiplay", ListenKey: "demo-listen-key", Premium: true}
}

// -----------------------------------------------------------------------------
// FakeClient — satisfies ui.AudioClient
// -----------------------------------------------------------------------------

// FakeClient serves channels from embedded JSON. StreamURL returns a synthetic
// URL the FakePlayer recognises; CurrentlyPlaying rotates through canned tracks.
type FakeClient struct {
	channelsByNetwork map[string][]audioaddict.Channel
	trackByChannelID  map[int64]string
}

// NewClient loads the embedded fixtures.
func NewClient() *FakeClient {
	c := &FakeClient{
		channelsByNetwork: map[string][]audioaddict.Channel{},
		trackByChannelID:  map[int64]string{},
	}
	for _, n := range audioaddict.Networks {
		raw, err := fixturesFS.ReadFile("fixtures/channels_" + n.Slug + ".json")
		if err != nil {
			// Some networks don't have a fixture; that's intentional.
			continue
		}
		var chs []audioaddict.Channel
		if err := json.Unmarshal(raw, &chs); err == nil {
			c.channelsByNetwork[n.Slug] = chs
		}
	}
	raw, err := fixturesFS.ReadFile("fixtures/tracks.json")
	if err == nil {
		var rawMap map[string]string
		if err := json.Unmarshal(raw, &rawMap); err == nil {
			for k, v := range rawMap {
				if id, err := strconv.ParseInt(k, 10, 64); err == nil {
					c.trackByChannelID[id] = v
				}
			}
		}
	}
	return c
}

// Channels implements ui.AudioClient.
func (c *FakeClient) Channels(_ context.Context, network string) ([]audioaddict.Channel, error) {
	out, ok := c.channelsByNetwork[network]
	if !ok {
		return nil, audioaddict.ErrNotFound
	}
	return out, nil
}

// StreamURL implements ui.AudioClient.
func (c *FakeClient) StreamURL(_ context.Context, network, channelKey, _ string, _ audioaddict.Quality) (string, error) {
	chs, ok := c.channelsByNetwork[network]
	if !ok {
		return "", audioaddict.ErrNotFound
	}
	for _, ch := range chs {
		if ch.Key == channelKey {
			return fmt.Sprintf("demo://%s/%s", network, channelKey), nil
		}
	}
	return "", audioaddict.ErrNotFound
}

// CurrentlyPlaying implements ui.AudioClient. Returns the canned sample
// track for the given channel ID (or a generic placeholder).
func (c *FakeClient) CurrentlyPlaying(_ context.Context, _ string, channelID int64) (audioaddict.Track, error) {
	raw, ok := c.trackByChannelID[channelID]
	if !ok {
		raw = "Demo Artist - Demo Track"
	}
	t := audioaddict.Track{Track: raw}
	if a, b, ok := splitTrack(raw); ok {
		t.Artist = a
		t.Title = b
	}
	return t, nil
}

func splitTrack(s string) (string, string, bool) {
	for i := 0; i < len(s)-2; i++ {
		if s[i] == ' ' && s[i+1] == '-' && s[i+2] == ' ' {
			return s[:i], s[i+3:], true
		}
	}
	return "", "", false
}

// -----------------------------------------------------------------------------
// FakePlayer — satisfies ui.AudioPlayer
// -----------------------------------------------------------------------------

// FakePlayer is a no-op player that still emits the state transitions a real
// mpv-backed player would. The UI feels alive even though no audio plays.
type FakePlayer struct {
	events  chan player.Event
	state   atomic.Int32
	mu      sync.Mutex
	lastURL string
	closed  bool
}

// NewPlayer returns a started FakePlayer. The context is honored for cleanup.
func NewPlayer(ctx context.Context) (*FakePlayer, error) {
	p := &FakePlayer{events: make(chan player.Event, 16)}
	p.state.Store(int32(player.StateIdle))
	go func() {
		<-ctx.Done()
		_ = p.Close()
	}()
	return p, nil
}

// Play emits Loading then Playing after a short delay so the UI shows the
// loading glyph briefly.
func (p *FakePlayer) Play(url string) error {
	p.mu.Lock()
	p.lastURL = url
	p.mu.Unlock()
	p.setState(player.StateLoading, url, nil)
	go func() {
		time.Sleep(200 * time.Millisecond)
		p.setState(player.StatePlaying, url, nil)
	}()
	return nil
}

// Pause sets the state to Paused.
func (p *FakePlayer) Pause() error { p.setState(player.StatePaused, "", nil); return nil }

// Resume sets the state to Playing.
func (p *FakePlayer) Resume() error { p.setState(player.StatePlaying, "", nil); return nil }

// Stop sets the state to Idle.
func (p *FakePlayer) Stop() error { p.setState(player.StateIdle, "", nil); return nil }

// SetVolume is a no-op (fake player has no audio).
func (p *FakePlayer) SetVolume(_ int) error { return nil }

// Events returns the state event stream.
func (p *FakePlayer) Events() <-chan player.Event { return p.events }

// State returns the current state.
func (p *FakePlayer) State() player.State { return player.State(p.state.Load()) }

// Close shuts the player down.
func (p *FakePlayer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	close(p.events)
	return nil
}

func (p *FakePlayer) setState(s player.State, url string, err error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	p.state.Store(int32(s))
	if url == "" {
		p.mu.Lock()
		url = p.lastURL
		p.mu.Unlock()
	}
	select {
	case p.events <- player.Event{State: s, URL: url, Err: err}:
	default:
	}
}
