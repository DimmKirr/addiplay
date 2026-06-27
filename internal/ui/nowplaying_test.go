package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/fanart"
)

// channelWithArt returns a channel with a known images.square template URL
// so we can assert against substring matches in the resolved URL.
func channelWithArt() audioaddict.Channel {
	return audioaddict.Channel{
		ID:  90,
		Key: "classictrance",
		Image: audioaddict.Image{
			Square:  "//cdn-images.audioaddict.com/CHAN/SQUARE.png{?size,height,width,quality,pad}",
			Default: "//cdn-images.audioaddict.com/CHAN/DEFAULT.png{?size,height,width,quality,pad}",
		},
	}
}

// TestPreferredFanartSource_preferTrackOverChannel locks in the rule that
// per-track album cover wins over channel art whenever the track has one.
// The wireframe + user requirement is "song thumbnail when a song is open".
func TestPreferredFanartSource_preferTrackOverChannel(t *testing.T) {
	ch := channelWithArt()
	track := audioaddict.Track{
		Artist: "FilterFunk",
		Title:  "S.O.S.",
		ArtURL: "//cdn-images.audioaddict.com/TRACK/077cf5ef.webp",
	}
	got := preferredFanartSource(track, ch, fanart.ModeASCII)
	if !strings.Contains(got, "TRACK/077cf5ef.webp") {
		t.Fatalf("expected resolved URL to use track art_url; got %q", got)
	}
	if strings.Contains(got, "CHAN/") {
		t.Errorf("track art_url present but channel image was used instead; got %q", got)
	}
	// Sanity: size + quality params are appended for server-side thumbnail.
	if !strings.Contains(got, "size=") || !strings.Contains(got, "quality=75") {
		t.Errorf("expected size= and quality=75 in URL; got %q", got)
	}
}

// TestPreferredFanartSource_fallsBackToChannelArt covers DJ mixes / ads
// (track.ArtURL is empty) — channel image should fill in.
func TestPreferredFanartSource_fallsBackToChannelArt(t *testing.T) {
	ch := channelWithArt()
	got := preferredFanartSource(audioaddict.Track{}, ch, fanart.ModeASCII)
	if !strings.Contains(got, "CHAN/SQUARE.png") {
		t.Errorf("expected channel square art; got %q", got)
	}
}

// TestPreferredFanartSource_returnsEmptyWhenNothingAvailable covers the
// no-art channel case — caller (refreshFanart) should treat this as a
// placeholder situation, never panic.
func TestPreferredFanartSource_returnsEmptyWhenNothingAvailable(t *testing.T) {
	ch := audioaddict.Channel{ID: 1, Key: "x"} // no Image, no AssetURL
	if got := preferredFanartSource(audioaddict.Track{}, ch, fanart.ModeASCII); got != "" {
		t.Errorf("expected empty URL for art-less channel; got %q", got)
	}
}

// TestRefreshFanart_doesNotClearStaleEscapeMidLoad pins the no-flash
// behaviour. When refreshFanart is called with a new URL that needs a
// fresh fetch, the previously-rendered escape MUST stay in place — so
// the user sees stale art (channel) instead of placeholder until the
// new art (album) is ready. Reverting this guard re-introduces the
// "channel art flashes then disappears" bug the user reported.
func TestRefreshFanart_doesNotClearStaleEscapeMidLoad(t *testing.T) {
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	m := Model{fanartCache: fanart.NewCache()}
	ch := channelWithArt()

	// Initial channel art load.
	_ = m.refreshFanart(audioaddict.Track{}, ch)
	const stale = "STALE_CHANNEL_ESCAPE"
	m.fanartEscape = stale

	// Now a track update arrives with a different art URL — a new fetch
	// is needed but the old escape must remain visible until it lands.
	track := audioaddict.Track{ArtURL: "//cdn-images.audioaddict.com/TRACK/x.webp"}
	if cmd := m.refreshFanart(track, ch); cmd == nil {
		t.Fatal("expected new fetch Cmd for changed track art")
	}
	if m.fanartEscape != stale {
		t.Errorf("fanartEscape was cleared mid-load (would cause flash); got %q want stale %q",
			m.fanartEscape, stale)
	}
}

// TestFetchFanartCmd_kittyUsesPlaceholder pins the Now Playing pane to
// the Unicode-placeholder protocol when in Kitty mode. Reverting to the
// direct-placement `a=T` form (without U=1) makes lipgloss measure the
// escape as 0 cells tall — the title and track text get written on top
// of the image instead of below it. The user reported this bug AFTER
// Kitty cards started working but Now Playing was still on direct
// placement. Drives FetchPlaceholder via an httptest server.
func TestFetchFanartCmd_kittyUsesPlaceholder(t *testing.T) {
	// Serve a small PNG so the fetch pipeline succeeds end-to-end.
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			src.Set(x, y, color.RGBA{R: 80, G: 120, B: 200, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBuf.Bytes())
	}))
	defer srv.Close()

	cache := fanart.NewCache()
	cmd := fetchFanartCmd(context.Background(), cache, srv.URL, 30, 15, nowPlayingFanartID, fanart.ModeKitty)
	msg := cmd()
	ready, ok := msg.(fanartReadyMsg)
	if !ok {
		t.Fatalf("expected fanartReadyMsg; got %T", msg)
	}
	if ready.err != nil {
		t.Fatalf("fetch failed: %v", ready.err)
	}
	// Placeholder protocol uses U=1 in transmit + U+10EEEE in the block.
	if !strings.Contains(ready.escape, "U=1") {
		t.Errorf("Now Playing Kitty escape missing U=1 (virtual placement) — direct-placement regression; text will overlap image")
	}
	if !strings.Contains(ready.escape, "\U0010EEEE") {
		t.Errorf("Now Playing escape missing U+10EEEE placeholder cells — direct-placement regression")
	}
	if !strings.Contains(ready.escape, fmt.Sprintf("i=%d", nowPlayingFanartID)) {
		t.Errorf("expected stable image id %d in escape", nowPlayingFanartID)
	}
}

// TestRefreshFanart_usesStableKittyID is the regression guard for the
// "channel art persists under album art" bug. Per the Kitty graphics
// protocol, distinct image IDs allocate distinct placements that stack.
// To make the new image actually REPLACE the old, the id must stay the
// same across refresh calls — Kitty then overwrites the image bytes at
// that id and the single existing placement auto-updates.
//
// Reverting to a URL-derived id (idFromURL or similar) re-introduces
// the bug. This test fails immediately if that happens.
func TestRefreshFanart_usesStableKittyID(t *testing.T) {
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii") // any non-None mode is fine
	t.Setenv("COLORTERM", "truecolor")

	m := Model{fanartCache: fanart.NewCache()}
	ch := channelWithArt()

	// First call — channel art.
	_ = m.refreshFanart(audioaddict.Track{}, ch)
	id1 := m.fanartID

	// Second call — same channel, different track art URL.
	track := audioaddict.Track{ArtURL: "//cdn-images.audioaddict.com/TRACK/aaa.webp"}
	_ = m.refreshFanart(track, ch)
	id2 := m.fanartID

	if id1 != id2 {
		t.Errorf("Kitty image id must be stable across refresh calls; got %d then %d", id1, id2)
	}
	if id1 != nowPlayingFanartID {
		t.Errorf("expected nowPlayingFanartID (%d); got %d", nowPlayingFanartID, id1)
	}
}

// TestStreamThenTrack_endsOnTrackArt simulates the actual production flow:
// stream starts → channel art Cmd issued + cached → trackUpdateMsg arrives
// with track.ArtURL → fanartSourceURL must end up as the TRACK URL, not the
// channel URL.
//
// This is the regression guard for the screenshot bug: user played
// rockradio/classicrock; the API returned a valid art_url for
// "Rolling Stones - Under My Thumb" but the Now Playing pane stayed on
// the channel image.
func TestStreamThenTrack_endsOnTrackArt(t *testing.T) {
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	m := Model{fanartCache: fanart.NewCache()}
	ch := audioaddict.Channel{
		ID:  143,
		Key: "classicrock",
		Image: audioaddict.Image{
			Square: "//cdn-images.audioaddict.com/2/5/0/0/3/7/250037906edac7b4fedec78add206977.png{?size,height,width,quality,pad}",
		},
		AssetURL: "//cdn-images.audioaddict.com/c/d/1/1/e/9/cd11e96692b5a9480123ff96159b0f7c.png",
	}

	// 1) Stream starts: refreshFanart called with empty Track + channel.
	cmd1 := m.refreshFanart(audioaddict.Track{}, ch)
	if cmd1 == nil {
		t.Fatal("expected initial fetch Cmd for channel art")
	}
	chURL := m.fanartSourceURL
	if !strings.Contains(chURL, "2500379") {
		t.Fatalf("step 1: expected channel URL recorded; got %q", chURL)
	}
	// Simulate the channel-art fetch completing.
	m.fanartEscape = "FAKE_CHANNEL_ESCAPE"

	// 2) Track arrives with art_url (the actual URL the live API returned).
	track := audioaddict.Track{
		ID:     1,
		Artist: "The Rolling Stones",
		Title:  "Under My Thumb",
		ArtURL: "//cdn-images.audioaddict.com/a/a/a/b/0/8/aaab088a09135e96b6412468af7a9a65.jpg",
	}
	cmd2 := m.refreshFanart(track, ch)
	if cmd2 == nil {
		t.Fatal("step 2: expected a NEW fetch Cmd for the track art")
	}

	// THE assertion. fanartSourceURL must now be the TRACK URL.
	if !strings.Contains(m.fanartSourceURL, "aaab088a") {
		t.Errorf("step 2: fanartSourceURL should switch to track art; got %q (track URL was dropped)", m.fanartSourceURL)
	}
	if m.fanartSourceURL == chURL {
		t.Error("step 2: fanartSourceURL is still the channel URL — track art will never render")
	}
	// Old escape stays visible until the new fetch resolves (no-flash contract).
	if m.fanartEscape != "FAKE_CHANNEL_ESCAPE" {
		t.Errorf("step 2: stale escape wiped (would cause flash); got %q", m.fanartEscape)
	}
}

// TestRefreshFanart_swapsOnNewTrackArt verifies that when a trackUpdateMsg
// arrives with a different art_url, refreshFanart updates fanartSourceURL
// and emits a fetch Cmd (we don't run it; just assert the cmd is non-nil).
// On Mode=None terminals, no fetch is launched.
func TestRefreshFanart_swapsOnNewTrackArt(t *testing.T) {
	// Force ASCII mode (works on any test runner; doesn't depend on $TERM).
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	m := Model{fanartCache: fanart.NewCache()}
	ch := channelWithArt()

	// First track → fetch command issued, source URL recorded.
	track1 := audioaddict.Track{ArtURL: "//cdn-images.audioaddict.com/TRACK/aaa.webp"}
	cmd := m.refreshFanart(track1, ch)
	if cmd == nil {
		t.Fatal("expected fetch Cmd for new track art; got nil")
	}
	first := m.fanartSourceURL
	if !strings.Contains(first, "TRACK/aaa.webp") {
		t.Errorf("fanartSourceURL not set to track art; got %q", first)
	}

	// Same track again → no new fetch (URL unchanged, escape cached).
	m.fanartEscape = "FAKE_ESCAPE"
	if c := m.refreshFanart(track1, ch); c != nil {
		t.Error("expected NO refetch when source URL unchanged")
	}

	// Different track → re-fetch.
	track2 := audioaddict.Track{ArtURL: "//cdn-images.audioaddict.com/TRACK/bbb.webp"}
	cmd2 := m.refreshFanart(track2, ch)
	if cmd2 == nil {
		t.Fatal("expected fetch Cmd when track art changes; got nil")
	}
	if m.fanartSourceURL == first {
		t.Error("fanartSourceURL should have updated to second track art")
	}
}
