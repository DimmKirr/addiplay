package ui_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/dimmkirr/addiplay/internal/demo"
	"github.com/dimmkirr/addiplay/internal/ui"
)

// driveDemo constructs the TUI exactly as `addiplay demo` would, plays the
// supplied key script through it (with a small pause between events so async
// commands like loadChannels / play settle), then returns the captured final
// frame.
func driveDemo(t *testing.T, script func(tm *teatest.TestModel)) string {
	t.Helper()
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := demo.NewClient()
	newPlayer := func(ctx context.Context) (ui.AudioPlayer, error) {
		return demo.NewPlayer(ctx)
	}
	tm := teatest.NewTestModel(t,
		ui.NewModel(ctx, demo.Creds(), client, newPlayer),
		teatest.WithInitialTermSize(140, 60),
	)
	tm.Send(tea.WindowSizeMsg{Width: 140, Height: 60})
	time.Sleep(300 * time.Millisecond) // let Init() commands settle

	if script != nil {
		script(tm)
	}
	time.Sleep(400 * time.Millisecond) // let the last actions render

	tm.Send(tea.QuitMsg{})
	_ = tm.Quit()
	out := tm.FinalOutput(t, teatest.WithFinalTimeout(2*time.Second))
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, out)
	return buf.String()
}

func sendKey(tm *teatest.TestModel, r rune) {
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	time.Sleep(40 * time.Millisecond)
}

func assertContains(t *testing.T, frame string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(frame, w) {
			t.Errorf("final frame missing %q\n--- frame ---\n%s", w, frame)
			return
		}
	}
}

func TestDemo_mainScreen_showsHeaderAndChannels(t *testing.T) {
	frame := driveDemo(t, nil)
	assertContains(t, frame,
		"addiplay",
		"DI.fm",
		"ALL",
		"Classic EuroDance",
		"vol 65%",
	)
}

func TestDemo_navigateAndPlay_setsPlayingState(t *testing.T) {
	frame := driveDemo(t, func(tm *teatest.TestModel) {
		sendKey(tm, 'j')                        // down
		tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // play
		time.Sleep(400 * time.Millisecond)      // wait for Loading → Playing transition (200ms in fake)
	})
	assertContains(t, frame,
		"Vocal Trance",   // selected channel
		"Above & Beyond", // canned now-playing track for channel id 2
	)
}

func TestDemo_networkPicker_swapsTheme(t *testing.T) {
	frame := driveDemo(t, func(tm *teatest.TestModel) {
		sendKey(tm, 'n')                        // open network picker
		sendKey(tm, 'j')                        // down → RadioTunes
		sendKey(tm, 'j')                        // down → RockRadio
		tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // pick
		time.Sleep(300 * time.Millisecond)      // wait for channels to load
	})
	assertContains(t, frame,
		"RockRadio",
		"Classic Rock",
		"80s Rock",
	)
}

func TestDemo_search_filtersChannels(t *testing.T) {
	// teatest's FinalOutput is the entire byte stream, not a single screen
	// snapshot, so negative assertions ("must not contain X") are unsafe —
	// X may appear in an earlier frame. Assert the filtered count instead.
	frame := driveDemo(t, func(tm *teatest.TestModel) {
		sendKey(tm, '/')
		for _, r := range "amb" {
			sendKey(tm, r)
		}
		tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	})
	assertContains(t, frame,
		"Ambient",
		"1 channels", // right-pane header proves the filter is active
	)
}

func TestDemo_favorite_toggleAndTab(t *testing.T) {
	frame := driveDemo(t, func(tm *teatest.TestModel) {
		sendKey(tm, 'f')                      // favorite Classic EuroDance
		tm.Send(tea.KeyMsg{Type: tea.KeyTab}) // switch to Favorites tab
		time.Sleep(80 * time.Millisecond)
	})
	assertContains(t, frame,
		"FAVORITES",
		"Classic EuroDance",
	)
}

func TestDemo_volume_changeReflectedInStatus(t *testing.T) {
	frame := driveDemo(t, func(tm *teatest.TestModel) {
		for i := 0; i < 3; i++ { // +15 → 80%
			sendKey(tm, '+')
		}
	})
	assertContains(t, frame, "vol 80%")
}
