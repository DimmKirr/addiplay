package ui

import (
	"bytes"
	"context"
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

// Avoid colliding with the standard min() shadow used by other tests.
func init() { _ = context.Background }
