package ui

import (
	"testing"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/fanart"
)

// TestRefreshFanart_channelSwitch_clearsStaleEscape reproduces DIMM-420
// symptom #1. When the user switches to channel B, the streamPlayingMsg
// handler calls refreshFanart with an empty track. Today that path
// dispatches a new fetch but leaves the previously-painted escape on
// screen — and if the new fetch fails (network blip, decode error,
// 404 from the CDN) the channel-A art stays visible permanently while
// the player is on channel B.
//
// The fix: when refreshFanart is invoked with an empty track AND the
// source URL is changing, clear the prior escape so a failed fetch
// degrades to the placeholder, not stale-wrong-art.
//
// Note: track-update WITHIN the same channel must still preserve the
// stale escape (otherwise the user sees a placeholder flash every time
// the playing track changes — the "art flashes and disappears" symptom
// the no-clear comment was originally written to prevent).
func TestRefreshFanart_channelSwitch_clearsStaleEscape(t *testing.T) {
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	m := newTestModel(t)
	// Stand-in for the previous channel/track's painted art.
	m.fanartEscape = "STALE_ESCAPE_FROM_CHANNEL_A"
	m.fanartSourceURL = "https://cdn.example/channelA.png?size=300x300&quality=75"

	// Simulate streamPlayingMsg for a NEW channel (track is empty,
	// channel has different art URL).
	newChannel := audioaddict.Channel{
		ID:   2,
		Key:  "channelB",
		Name: "Channel B",
		Image: audioaddict.Image{
			Square: "https://cdn.example/channelB.png{?size,height,width,quality,pad}",
		},
	}

	cmd := m.refreshFanart(audioaddict.Track{}, newChannel)

	if cmd == nil {
		t.Error("refreshFanart returned nil cmd — should have dispatched a fetch for new channel art")
	}
	if m.fanartEscape != "" {
		t.Errorf("fanartEscape = %q after channel switch; want \"\" so failed fetch degrades to placeholder, not wrong art",
			m.fanartEscape)
	}
}

// TestRefreshFanart_trackUpdate_preservesEscape locks in the existing
// no-clear behavior on track-update within the same channel. Without
// this guard the original "channel art flashes then disappears" bug
// returns: streamPlayingMsg paints channel art, trackUpdateMsg ~150ms
// later would clear it before the album-art fetch completes.
func TestRefreshFanart_trackUpdate_preservesEscape(t *testing.T) {
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	m := newTestModel(t)
	// Channel art currently painted.
	m.fanartEscape = "CHANNEL_ART_ESCAPE"
	m.fanartSourceURL = "https://cdn.example/channelA.png?size=300x300&quality=75"

	channelA := audioaddict.Channel{
		ID:   1,
		Key:  "channelA",
		Name: "Channel A",
		Image: audioaddict.Image{
			Square: "https://cdn.example/channelA.png{?size,height,width,quality,pad}",
		},
	}
	// trackUpdateMsg arrives with the now-playing track and its art URL.
	newTrack := audioaddict.Track{
		ID:     42,
		Artist: "Test Artist",
		Title:  "Test Track",
		ArtURL: "//cdn.example/track42.png",
	}

	cmd := m.refreshFanart(newTrack, channelA)
	if cmd == nil {
		t.Fatal("refreshFanart returned nil cmd — should have dispatched fetch for track art")
	}
	if m.fanartEscape != "CHANNEL_ART_ESCAPE" {
		t.Errorf("fanartEscape = %q after track-update within same channel; want CHANNEL_ART_ESCAPE preserved (no flash)",
			m.fanartEscape)
	}
}

// Sanity-check fanart.DetectMode wiring in this test file.
func TestRefreshFanart_modeNone_clearsEverything(t *testing.T) {
	t.Setenv("ADDIPLAY_NO_FANART", "1")
	if fanart.DetectMode() != fanart.ModeNone {
		t.Skip("ADDIPLAY_NO_FANART not honoured in this env")
	}
	m := newTestModel(t)
	m.fanartEscape = "ANYTHING"
	m.fanartSourceURL = "https://x"
	cmd := m.refreshFanart(audioaddict.Track{}, audioaddict.Channel{Key: "ch", Image: audioaddict.Image{Square: "//cdn/x.png{?size}"}})
	if cmd != nil {
		t.Error("expected nil cmd when fanart disabled")
	}
	if m.fanartEscape != "" || m.fanartSourceURL != "" {
		t.Errorf("escape=%q src=%q; want both empty when fanart disabled", m.fanartEscape, m.fanartSourceURL)
	}
}
