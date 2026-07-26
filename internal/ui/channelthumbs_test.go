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
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/fanart"
)

// TestRenderCard_usesCachedThumbWhenAvailable verifies the card renderer
// pulls the per-channel ASCII thumbnail from Model.channelThumbs when
// present, rather than drawing the colored-swatch placeholder.
func TestRenderCard_usesCachedThumbWhenAvailable(t *testing.T) {
	m := newTestModel(t)
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "trance", Name: "Trance"},
	}
	const marker = "ASCII_THUMB_MARKER_XYZ"
	m.channelThumbs["trance"] = marker

	out := renderCard(m, m.channels[0], true /*selected*/, false /*playing*/, 80)
	if !strings.Contains(out, marker) {
		t.Errorf("expected cached thumb %q in card output; got:\n%s", marker, out)
	}
}

// TestRenderCard_fallsBackToPlaceholderWhileFetching ensures cards still
// draw a swatch column while the thumb fetch is in flight (empty string
// in the cache map signals in-flight) — they must never collapse to a
// 0-width thumb that breaks the card layout.
func TestRenderCard_fallsBackToPlaceholderWhileFetching(t *testing.T) {
	m := newTestModel(t)
	m.channels = []audioaddict.Channel{{ID: 1, Key: "trance", Name: "Trance"}}
	m.channelThumbs["trance"] = "" // in-flight marker
	out := renderCard(m, m.channels[0], false, false, 80)
	if strings.Count(out, "\n") < cardHeight-2 {
		t.Errorf("card should be at least %d rows tall even without thumb; got:\n%s", cardHeight-2, out)
	}
}

// TestKickoffVisibleThumbs_fetchesViaHTTPServer drives the full pipe:
// httptest server returns a real PNG, the dispatched Cmd downloads +
// half-block-encodes it, and the resulting channelThumbReadyMsg fills
// the cache. Locks the wire format: fetched thumbs are non-empty and
// contain ANSI truecolor escapes (half-block characters).
func TestKickoffVisibleThumbs_fetchesViaHTTPServer(t *testing.T) {
	// Force ASCII mode so DetectMode() returns non-None on any runner.
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	// Stand up a server that returns a 32×32 magenta PNG for any GET.
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			src.Set(x, y, color.RGBA{R: 200, G: 40, B: 200, A: 255})
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

	m := newTestModel(t)
	// Force the channel's PreferredFanartURL to point at our server. We
	// inject a fake template since AudioAddict's template syntax is
	// {?size,...} which ResolveImageURL strips before appending params.
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "trance", Name: "Trance", Image: audioaddict.Image{Square: srv.URL + "/sq.png{?size,height,width,quality,pad}"}},
	}
	m.selIdx = 0
	m.height = 40
	m.width = 140
	m.channelThumbs = map[string]string{}

	cmds := m.kickoffVisibleThumbs()
	if len(cmds) == 0 {
		t.Fatal("expected at least one fetch Cmd for visible card")
	}

	// Run the first Cmd; it should resolve to a channelThumbReadyMsg with
	// a non-empty escape carrying half-block + ANSI SGR codes.
	msg := waitForMsg(t, cmds[0], 5*time.Second)
	ready, ok := msg.(channelThumbReadyMsg)
	if !ok {
		t.Fatalf("expected channelThumbReadyMsg; got %T = %+v", msg, msg)
	}
	if ready.key != "trance" {
		t.Errorf("ready.key = %q, want trance", ready.key)
	}
	if ready.escape == "" {
		t.Fatal("ready.escape is empty — fetch failed silently")
	}
	if !strings.Contains(ready.escape, "▀") {
		t.Errorf("expected half-block ▀ in escape; got first 200 chars: %q", ready.escape[:min(200, len(ready.escape))])
	}
	if !strings.Contains(ready.escape, "\x1b[38;2;") {
		t.Errorf("expected truecolor SGR escape in output; got first 200 chars: %q", ready.escape[:min(200, len(ready.escape))])
	}
}

// TestKickoffVisibleThumbs_noopWhenFanartDisabled verifies we don't fire
// network requests on terminals where fanart can't render anyway.
func TestKickoffVisibleThumbs_noopWhenFanartDisabled(t *testing.T) {
	t.Setenv("ADDIPLAY_NO_FANART", "1")
	if fanart.DetectMode() != fanart.ModeNone {
		t.Skip("ADDIPLAY_NO_FANART not honoured in this env")
	}
	m := newTestModel(t)
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "trance", Image: audioaddict.Image{Square: "//cdn-images/example.png{?size}"}},
	}
	if cmds := m.kickoffVisibleThumbs(); len(cmds) != 0 {
		t.Errorf("expected no Cmds when fanart mode is None; got %d", len(cmds))
	}
}

// waitForMsg runs a Cmd inline (synchronously) and returns its tea.Msg.
// Real bubbletea runs Cmds on a goroutine pool; for tests we just invoke.
func waitForMsg(t *testing.T, cmd tea.Cmd, _ time.Duration) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("cmd is nil")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("cmd did not complete within 5s")
		return nil
	}
}

// TestWindowResize_kicksOffThumbsForNewlyVisibleCards verifies that when a
// WindowSizeMsg arrives AFTER channels have loaded (e.g. the app started in
// a background window and is now focused), thumbnails are fetched for cards
// that became visible due to the larger viewport. Without the re-kick in
// handleDomain's WindowSizeMsg branch, cards outside the initial (height=0)
// viewport would stay blank until the user scrolled.
func TestWindowResize_kicksOffThumbsForNewlyVisibleCards(t *testing.T) {
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	m := newTestModel(t)
	channels := make([]audioaddict.Channel, 20)
	for i := range channels {
		channels[i] = audioaddict.Channel{
			ID:   int64(i + 1),
			Key:  fmt.Sprintf("ch%02d", i),
			Name: fmt.Sprintf("Channel %02d", i),
			Image: audioaddict.Image{
				Square: fmt.Sprintf("//cdn.example/%02d.png{?size,height,width,quality,pad}", i),
			},
		}
	}

	// Step 1: Load channels at a small height (simulates channels arriving
	// before the terminal reports its actual size — height is 0 or very
	// small when the window is not in focus).
	m.width = 140
	m.height = 0
	m.channels = channels
	m.channelThumbs = map[string]string{}
	_ = m.kickoffVisibleThumbs()
	thumbsBefore := len(m.channelThumbs)

	// Step 2: WindowSizeMsg arrives with a real size — simulates the
	// terminal window gaining focus and reporting its actual dimensions.
	m2, cmd := m.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
	mm := m2.(Model)

	// After resize, more cards should be visible. The handler should have
	// dispatched kickoffVisibleThumbs, populating in-flight markers for
	// newly-visible cards. Count how many entries we have now.
	thumbsAfter := len(mm.channelThumbs)

	if thumbsAfter <= thumbsBefore {
		t.Errorf("WindowSizeMsg should trigger thumbnail loads for newly-visible cards; "+
			"before=%d after=%d (no new fetches)", thumbsBefore, thumbsAfter)
	}
	if cmd == nil {
		t.Error("expected batch Cmd from WindowSizeMsg with pending thumb fetches")
	}
}

// TestFanartLoads_withoutUserInteraction verifies that the Now Playing
// fanart loads correctly through domain events alone — no key presses
// required. Simulates the scenario where the app is playing in the
// background (not focused) and a track change triggers a fanart refresh.
func TestFanartLoads_withoutUserInteraction(t *testing.T) {
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	m := newTestModel(t)
	ch := audioaddict.Channel{
		ID:  90,
		Key: "classictrance",
		Image: audioaddict.Image{
			Square: "//cdn-images.audioaddict.com/CHAN/SQUARE.png{?size,height,width,quality,pad}",
		},
	}
	m.channels = []audioaddict.Channel{ch}
	m.currentChannel = "classictrance"
	m.playingNetwork = "di"
	m.currentNetwork = "di"

	// Step 1: streamPlayingMsg arrives (domain event, no user interaction).
	m2, cmd1 := m.Update(streamPlayingMsg{network: "di", channel: ch})
	mm := m2.(Model)

	// Should have dispatched a fanart fetch command.
	if mm.fanartSourceURL == "" {
		t.Error("streamPlayingMsg should set fanartSourceURL")
	}
	if cmd1 == nil {
		t.Error("streamPlayingMsg should return commands (track fetch, fanart, etc.)")
	}

	// Step 2: Simulate fanart arriving (domain event).
	const fakeEscape = "FAKE_FANART_ESCAPE_DATA"
	m3, _ := mm.Update(fanartReadyMsg{url: mm.fanartSourceURL, escape: fakeEscape})
	mm2 := m3.(Model)

	if mm2.fanartEscape != fakeEscape {
		t.Errorf("fanartReadyMsg should update fanartEscape; got %q want %q",
			mm2.fanartEscape, fakeEscape)
	}

	// Step 3: Track change arrives (another domain event, no user interaction).
	track := audioaddict.Track{
		ID:     42,
		Artist: "Cosmic Gate",
		Title:  "Exploration of Space",
		ArtURL: "//cdn-images.audioaddict.com/TRACK/abc123.webp",
	}
	m4, cmd2 := mm2.Update(trackUpdateMsg{
		channelID: 90,
		track:     track,
		gen:       mm2.trackTickGen,
	})
	mm3 := m4.(Model)

	// Should have updated the fanart source to the track art.
	if !strings.Contains(mm3.fanartSourceURL, "abc123") {
		t.Errorf("trackUpdateMsg should switch fanartSourceURL to track art; got %q",
			mm3.fanartSourceURL)
	}
	if cmd2 == nil {
		t.Error("trackUpdateMsg with new art should dispatch a fanart fetch")
	}

	// Old escape should remain visible (no-flash invariant).
	if mm3.fanartEscape != fakeEscape {
		t.Errorf("stale escape should persist during fetch; got %q", mm3.fanartEscape)
	}
}

// Avoid colliding with the standard min() shadow used by other tests.
func init() { _ = context.Background }
