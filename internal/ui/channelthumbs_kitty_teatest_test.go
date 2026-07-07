package ui_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/demo"
	"github.com/dimmkirr/addiplay/internal/ui"
)

// TestTTY_kittyMode_channelCardsCarryPlaceholders reproduces DIMM-420
// symptom #2: in Kitty mode, channel cards should render the Unicode-
// placeholder protocol (U+10EEEE cells + a=T,U=1 transmit escape).
//
// If this test PASSES, the dispatch + encode pipeline is correct and
// any visible "no images in cards" is a TERMINAL/tmux passthrough
// issue (the escape leaves addiplay correctly but the terminal stack
// strips it before rendering).
//
// If this test FAILS, the rendered frame has no placeholder cells —
// meaning the bug is inside addiplay (mode detection, cmd dispatch,
// or render skipping the escape).
func TestTTY_kittyMode_channelCardsCarryPlaceholders(t *testing.T) {
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())
	t.Setenv("ADDIPLAY_FANART_MODE", "kitty") // force Kitty mode

	pngBytes := makeTinyPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	const total = 6
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

	tm := teatest.NewTestModel(t,
		ui.NewModel(ctx, creds.Session{Email: "tester@example", ListenKey: "k"}, client, newPlayer),
		teatest.WithInitialTermSize(140, 60),
	)
	tm.Send(tea.WindowSizeMsg{Width: 140, Height: 60})
	time.Sleep(800 * time.Millisecond) // let initial channels load + fetch + render

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

	// Kitty placeholder protocol signatures in the rendered frame.
	hasTransmit := strings.Contains(frame, "_Ga=T,U=1")
	hasPlaceholderCell := strings.Contains(frame, "\U0010EEEE")
	if !hasTransmit {
		t.Errorf("Kitty mode: no a=T,U=1 transmit escape in final frame — channel thumbnails never uploaded\nframe tail:\n%s",
			lastLines(frame, 20))
	}
	if !hasPlaceholderCell {
		t.Errorf("Kitty mode: no U+10EEEE placeholder cells in final frame — channel cards aren't rendering the placeholder block\nframe tail:\n%s",
			lastLines(frame, 20))
	}

	// DIMM-420 #2 deeper check: the transmit chunks of each image MUST
	// precede their placeholder cells in the byte stream. Otherwise the
	// terminal sees cells referencing an image id that hasn't been
	// uploaded yet → renders nothing. We assert this by walking the
	// rendered frame: every U+10EEEE cell must be preceded (in the same
	// or an earlier byte position) by an `i=N` transmit declaration for
	// the matching image id (color-encoded into the cell's prior SGR).
	//
	// Simpler check: split the frame at the FIRST placeholder cell. The
	// prefix MUST contain a complete transmit escape (well-terminated
	// `\x1b\\`). If lipgloss/bubbletea injected anything BETWEEN the
	// transmit's `\x1b\\` terminator and the first U+10EEEE, the terminal
	// may not associate the cells with the right image.
	firstCellIdx := strings.Index(frame, "\U0010EEEE")
	if firstCellIdx > 0 {
		prefix := frame[:firstCellIdx]
		// Count complete APC escape sequences (\x1b_G...\x1b\\). For
		// chunked transmits, multiple per image are expected.
		opens := strings.Count(prefix, "\x1b_G")
		closes := strings.Count(prefix, "\x1b\\")
		if opens == 0 {
			t.Errorf("first placeholder cell at byte %d has NO preceding `\\x1b_G` transmit — image never uploaded before its cells render\nprefix tail (last 200 bytes): %q",
				firstCellIdx, lastN(prefix, 200))
		}
		if closes < opens {
			t.Errorf("transmit escape(s) before first cell are unterminated: %d openings, %d `\\x1b\\\\` terminators — terminal will skip", opens, closes)
		}
	}
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
