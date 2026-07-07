package ui_test

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/demo"
	"github.com/dimmkirr/addiplay/internal/ui"
)

// fastScrollFakeClient swaps the demo client's channel list for one
// pointed at an httptest server, so the teatest exercises the REAL
// fanart fetch + render pipeline (including the kickoffVisibleThumbs
// prefetch margin under attack from rapid keypresses).
type fastScrollFakeClient struct {
	*demo.FakeClient
	channels []audioaddict.Channel
}

func (c *fastScrollFakeClient) Channels(_ context.Context, _ string) ([]audioaddict.Channel, error) {
	return c.channels, nil
}

// TestTTY_fastScrollDownThenStop_finalCardRendersThumb drives the TUI
// in a small terminal (cardsPerView=1), fires 12 Down keystrokes back-
// to-back with no inter-key sleep (the failure mode users report), then
// waits for fetches to settle and verifies the final selected card's
// region carries a real terminal-rendered thumbnail (truecolor SGR).
//
// This is the teatest companion to the unit-level
// TestKickoffVisibleThumbs_fastScroll_dispatchesForEveryVisitedCard.
// Confirms the dispatch+render pipeline doesn't strand cards under fast
// scrolling. See DIMM-419.
func TestTTY_fastScrollDownThenStop_finalCardRendersThumb(t *testing.T) {
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	// 32×32 magenta PNG, served with a 20ms artificial delay so all
	// the fetches race the way they would against a real CDN.
	pngBytes := makeTinyPNG(t)
	var hits int64
	var hitsMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsMu.Lock()
		hits++
		hitsMu.Unlock()
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	// 20 channels, each pointing at our test server.
	const total = 20
	channels := make([]audioaddict.Channel, total)
	for i := range channels {
		channels[i] = audioaddict.Channel{
			ID:   int64(i + 1),
			Key:  fmt.Sprintf("ch%02d", i),
			Name: fmt.Sprintf("Channel %02d", i),
			Image: audioaddict.Image{
				Square: fmt.Sprintf("%s/sq-%02d.png{?size,height,width,quality,pad}", srv.URL, i),
			},
		}
	}

	client := &fastScrollFakeClient{FakeClient: demo.NewClient(), channels: channels}
	newPlayer := func(ctx context.Context) (ui.AudioPlayer, error) {
		return demo.NewPlayer(ctx)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Small TTY: height=20 → bodyH=9 → cardsPerView=1.
	tm := teatest.NewTestModel(t,
		ui.NewModel(ctx, creds.Session{Email: "tester@example", ListenKey: "k"}, client, newPlayer),
		teatest.WithInitialTermSize(80, 20),
	)
	tm.Send(tea.WindowSizeMsg{Width: 80, Height: 20})
	time.Sleep(300 * time.Millisecond) // Init settles, initial channels load

	// FAST SCROLL: 12 Down presses, NO sleep between — the exact pattern
	// a user generates by holding Down. Bubble Tea processes them as
	// separate KeyMsgs in order.
	for i := 0; i < 12; i++ {
		tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	}

	// Settle: allow all in-flight fetches to complete and one final
	// render to land.
	time.Sleep(800 * time.Millisecond)

	tm.Send(tea.QuitMsg{})
	_ = tm.Quit()

	out := tm.FinalOutput(t, teatest.WithFinalTimeout(2*time.Second))
	buf := make([]byte, 0, 1<<20)
	tmpBuf := make([]byte, 4096)
	for {
		n, err := out.Read(tmpBuf)
		if n > 0 {
			buf = append(buf, tmpBuf[:n]...)
		}
		if errors.Is(err, errFinalRead) || err != nil {
			break
		}
	}
	frame := string(buf)

	// The selected card (now at index 12) must carry a thumbnail —
	// detectable by the truecolor SGR escape and the half-block glyph
	// that fanart.EncodeASCII produces. Without DIMM-419 fixed, the
	// final card sits on a colored swatch (uses lipgloss.Background
	// only — no half-blocks).
	if !strings.Contains(frame, "▀") {
		t.Errorf("after fast-scroll, final frame contains no half-block ▀ glyphs — no card rendered a real thumbnail\nhits=%d\nframe tail:\n%s",
			hits, lastLines(frame, 30))
	}
	if hits < 5 {
		t.Errorf("expected at least 5 HTTP hits across fast-scrolled cards; got %d", hits)
	}
}

// makeTinyPNG encodes a 32×32 magenta square as PNG — small enough to
// keep the test fast.
func makeTinyPNG(t *testing.T) []byte {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			src.Set(x, y, color.RGBA{R: 220, G: 30, B: 200, A: 255})
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "tiny-*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, src); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return body
}

var errFinalRead = errors.New("done")

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
