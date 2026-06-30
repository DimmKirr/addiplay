package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/demo"
	"github.com/dimmkirr/addiplay/internal/player"
)

// failingPlayer wraps demo.FakePlayer and emits StateError on Play to
// reproduce the "mpv stream error" path purely with mock data.
type failingPlayer struct {
	*demo.FakePlayer
}

func (f *failingPlayer) Play(url string) error {
	// Tell the fake player to stop after a short delay to simulate mpv
	// accepting the loadfile but then failing to open the stream.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = f.Stop()
	}()
	return f.FakePlayer.Play(url)
}

func newTestModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	fake, _ := demo.NewPlayer(ctx)
	fp := &failingPlayer{FakePlayer: fake}
	m := NewModel(ctx, demo.Creds(), demo.NewClient(),
		func(_ context.Context) (AudioPlayer, error) { return fp, nil })
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
	mm := m2.(Model)
	// Populate channels so selectedChannel() can return one.
	mm.channels = []audioaddict.Channel{
		{ID: 1, Key: "vocaltrance", Name: "Vocal Trance"},
		{ID: 2, Key: "chillout", Name: "Chillout"},
	}
	mm.player = fp
	return mm
}

// TestToast_clearsWhenUserPicksAnotherChannel verifies that a stale error
// toast disappears the moment the user expresses intent to play a different
// channel (presses Enter). Without this, an "mpv stream error" from a prior
// channel lingers on the status bar after the user has moved on.
func TestToast_clearsWhenUserPicksAnotherChannel(t *testing.T) {
	m := newTestModel(t)
	m.toast = "play: mpv stream error"
	if !strings.Contains(m.View(), "mpv stream error") {
		t.Fatalf("precondition: toast should be visible; view:\n%s", m.View())
	}

	// User navigates to another channel and presses Enter.
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m3, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m3.(Model)

	if strings.Contains(m.View(), "mpv stream error") {
		t.Errorf("expected toast to clear after Enter; view:\n%s", m.View())
	}
}

// TestArtColumn_showsTrackPlaceholderWhenUnknown verifies that the right
// pane shows a clear placeholder (not a blank line) when a channel is
// playing but the track is still loading or unknown.
func TestArtColumn_showsTrackPlaceholderWhenUnknown(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.playingNetwork = "di"
	// currentTrack is zero-value (Track == "")

	view := m.View()
	if !strings.Contains(view, "Vocal Trance") {
		t.Fatalf("expected channel name in view; got:\n%s", view)
	}
	if !strings.Contains(view, "—") && !strings.Contains(view, "loading") {
		t.Errorf("expected a track placeholder when track unknown; view:\n%s", view)
	}
}

// TestStatusBar_showsErrorWhenPlayerStateError verifies that when mpv has
// errored, the status bar reflects that — the ✗ glyph appears.
func TestStatusBar_showsErrorWhenPlayerStateError(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.playingNetwork = "di"
	m.playerSt = player.StateError

	view := m.View()
	if !strings.Contains(view, "✗") {
		t.Errorf("expected error glyph ✗ when state=Error; view:\n%s", view)
	}
}

// TestHeader_showsLoggedInEmail verifies the header surfaces the user
// identity (so people running multiple AudioAddict accounts know which
// one is active and where the listen_key is sourced from).
func TestHeader_showsLoggedInEmail(t *testing.T) {
	m := newTestModel(t)
	view := m.View()
	// demo.Creds() returns "demo@addiplay"; in production cmd/tui.go
	// passes the actual creds.Load() result.
	if !strings.Contains(view, "👤") || !strings.Contains(view, "demo@addiplay") {
		t.Errorf("expected header to contain '👤 demo@addiplay'; view:\n%s", view)
	}
}

// TestArtColumn_alwaysHasFallbacks verifies the right-side art column
// shows useful content for every state — never the "channel name only,
// rest blank" mode the user reported for melodic-death-metal.
func TestArtColumn_alwaysHasFallbacks(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(m *Model)
		wantPhrase string // must appear in the rendered view
	}{
		{
			name: "playing, track info present → shows artist",
			setup: func(m *Model) {
				m.currentTrack.Artist = "Sash!"
				m.currentTrack.Title = "Encore une fois"
				m.playerSt = player.StatePlaying
			},
			wantPhrase: "Sash!",
		},
		{
			name:       "playing, no track info → '(no track info)' placeholder",
			setup:      func(m *Model) { m.playerSt = player.StatePlaying },
			wantPhrase: "(no track info)",
		},
		{
			name:       "loading → 'loading…' placeholder",
			setup:      func(m *Model) { m.playerSt = player.StateLoading },
			wantPhrase: "loading…",
		},
		{
			name:       "stream error → ' stream error '",
			setup:      func(m *Model) { m.playerSt = player.StateError },
			wantPhrase: "stream error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.currentChannel = "vocaltrance"
			m.playingNetwork = "di"
			// Force the art column to be rendered by setting a fanart escape;
			// otherwise renderBody falls back to single-pane for this test
			// width and we wouldn't see the art column at all.
			m.fanartEscape = ""
			tc.setup(&m)

			view := m.View()
			if !strings.Contains(view, tc.wantPhrase) {
				t.Errorf("want %q in view; got:\n%s", tc.wantPhrase, view)
			}
		})
	}
}
