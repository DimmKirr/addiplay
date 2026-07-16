package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/player"
)

// --- Header tests ---

func TestHeader_narrowTerminal_showsAdd(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	mm := m2.(Model)
	out := renderHeader(mm)
	if !strings.Contains(out, "add") {
		t.Errorf("narrow header should contain 'add'; got:\n%s", out)
	}
	if strings.Contains(out, "addiplay") {
		t.Errorf("narrow header should NOT contain full 'addiplay'; got:\n%s", out)
	}
}

func TestHeader_wideTerminal_showsAddiplay(t *testing.T) {
	m := newTestModel(t)
	out := renderHeader(m) // 140 cols from newTestModel
	if !strings.Contains(out, "addiplay") {
		t.Errorf("wide header should contain 'addiplay'; got:\n%s", out)
	}
}

func TestHeader_hintsOnly_questionAndQuit(t *testing.T) {
	m := newTestModel(t)
	out := renderHeader(m)
	if !strings.Contains(out, "[?]") {
		t.Errorf("header should contain [?] hint; got:\n%s", out)
	}
	if !strings.Contains(out, "[q]") {
		t.Errorf("header should contain [q] hint; got:\n%s", out)
	}
	for _, dup := range []string{"[n]", "[/] filter"} {
		if strings.Contains(out, dup) {
			t.Errorf("header should NOT contain %q (duplicates status bar); got:\n%s", dup, out)
		}
	}
}

func TestHeader_emailHidden_showsIconOnly(t *testing.T) {
	m := newTestModel(t)
	out := renderHeader(m)
	if strings.Contains(out, m.creds.Email) {
		t.Errorf("header should NOT show full email %q; got:\n%s", m.creds.Email, out)
	}
}

// --- Channel list tests ---

func TestChannelHeader_noInstructionText(t *testing.T) {
	m := newTestModel(t)
	out := renderChannels(m, 80, 40)
	for _, junk := range []string{"j/k move", "enter play", "/ search", "tab favs"} {
		if strings.Contains(out, junk) {
			t.Errorf("channel header should NOT contain instruction %q; got:\n%s", junk, out)
		}
	}
	if !strings.Contains(out, "channels") {
		t.Errorf("channel header should still show channel count; got:\n%s", out)
	}
}

func TestCard_nonPlaying_noMusicNotePlaceholder(t *testing.T) {
	m := newTestModel(t)
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "trance", Name: "Trance"},
	}
	out := renderCard(m, m.channels[0], false, false, 80)
	if strings.Contains(out, "♪ —") || strings.Contains(out, "♪ —") {
		t.Errorf("non-playing card should NOT show ♪ — placeholder; got:\n%s", out)
	}
}

func TestCard_loadingState_usesBrailleSpinner(t *testing.T) {
	m := newTestModel(t)
	m.playerSt = player.StateLoading
	m.currentChannel = "trance"
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "trance", Name: "Trance"},
	}
	out := renderCard(m, m.channels[0], true, true, 80)
	if strings.Contains(out, "◐") {
		t.Errorf("loading card should use braille spinner, not ◐; got:\n%s", out)
	}
}

// --- Now Playing tests ---

func TestNowPlaying_showsDescription(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "vocaltrance", Name: "Vocal Trance",
			DescriptionShort: "Uplifting vocal trance anthems"},
	}
	m.currentTrack = audioaddict.Track{
		ID: 42, Artist: "Above & Beyond", Title: "Sun & Moon",
	}
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "Uplifting vocal trance") {
		t.Errorf("now playing should show channel description; got:\n%s", out)
	}
}

func TestNowPlaying_trackTitleProminent(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.currentTrack = audioaddict.Track{
		ID: 42, Artist: "Above & Beyond", Title: "Sun & Moon",
	}
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "Sun & Moon") {
		t.Errorf("now playing should show track title; got:\n%s", out)
	}
	if !strings.Contains(out, "Above & Beyond") {
		t.Errorf("now playing should show artist; got:\n%s", out)
	}
}

func TestNowPlaying_progressBar_showsElapsed(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.playerSt = player.StatePlaying
	m.currentTrack = audioaddict.Track{
		ID: 42, Artist: "A", Title: "B", Duration: 300,
	}
	m.trackStartTime = time.Now().Add(-90 * time.Second)
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "1:30") {
		t.Errorf("progress bar should show ~1:30 elapsed; got:\n%s", out)
	}
	if !strings.Contains(out, "5:00") {
		t.Errorf("progress bar should show 5:00 duration; got:\n%s", out)
	}
	if !strings.Contains(out, "━") {
		t.Errorf("progress bar should contain filled bar character; got:\n%s", out)
	}
}

func TestNowPlaying_progressBar_pauseShowsFixed(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.playerSt = player.StatePaused
	m.currentTrack = audioaddict.Track{
		ID: 42, Artist: "A", Title: "B", Duration: 300,
	}
	m.trackPauseElapsed = 120 * time.Second
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "2:00") {
		t.Errorf("paused progress should show 2:00; got:\n%s", out)
	}
}

func TestNowPlaying_channelDirector(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "vocaltrance", Name: "Vocal Trance",
			ChannelDirector: "DJ Ano"},
	}
	m.currentTrack = audioaddict.Track{ID: 42, Artist: "A", Title: "B"}
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "curated by DJ Ano") {
		t.Errorf("now playing should show channel director; got:\n%s", out)
	}
}

func TestNowPlaying_modeBadge_live(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.currentTrack = audioaddict.Track{ID: 42, Artist: "A", Title: "B"}
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "live") {
		t.Errorf("now playing should show 'live' badge; got:\n%s", out)
	}
}

func TestNowPlaying_modeBadge_onDemand(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.currentTrack = audioaddict.Track{ID: 42, Artist: "A", Title: "B"}
	q := audioaddict.NewTrackQueue()
	q.Append([]audioaddict.RoutineTrack{
		{TrackID: 1, Track: json.RawMessage(`"a"`)},
		{TrackID: 2, Track: json.RawMessage(`"b"`)},
		{TrackID: 3, Track: json.RawMessage(`"c"`)},
	})
	q.Next()
	m.trackQueue = q
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "on-demand") {
		t.Errorf("now playing should show 'on-demand' badge; got:\n%s", out)
	}
	if !strings.Contains(out, "track 1 of 3") {
		t.Errorf("now playing should show queue position; got:\n%s", out)
	}
}

func TestNowPlaying_recentTracks(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.currentTrack = audioaddict.Track{ID: 42, Artist: "A", Title: "B"}
	m.recentTracks = []audioaddict.Track{
		{Track: "Sash! - Encore Une Fois"},
		{Track: "ATB - 9pm"},
	}
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "recently on this channel") {
		t.Errorf("now playing should show recent tracks header; got:\n%s", out)
	}
	if !strings.Contains(out, "Sash!") {
		t.Errorf("now playing should show recent track; got:\n%s", out)
	}
}

func TestNowPlaying_voteCount(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.currentTrack = audioaddict.Track{ID: 42, Artist: "Above & Beyond", Title: "Sun & Moon"}
	m.voteUp = 847
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "847") {
		t.Errorf("now playing should show vote count; got:\n%s", out)
	}
}

func TestNowPlaying_accentBorder(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.currentTrack = audioaddict.Track{ID: 42, Artist: "A", Title: "B"}
	out := m.renderNowPlaying(36, 40)
	// Accent border — check that the pane renders (basic sanity).
	if !strings.Contains(out, "╭") || !strings.Contains(out, "╰") {
		t.Errorf("now playing pane should have rounded border corners; got:\n%s", out)
	}
}

func TestNowPlaying_descriptionWraps(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "vocaltrance", Name: "Vocal Trance",
			Description: "The best vocal trance music featuring soaring female vocals and uplifting melodies that transport you to another world"},
	}
	m.currentTrack = audioaddict.Track{ID: 42, Artist: "A", Title: "B"}
	out := m.renderNowPlaying(36, 40)
	if !strings.Contains(out, "vocal trance") {
		t.Errorf("now playing should show wrapped description; got:\n%s", out)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00"},
		{30 * time.Second, "0:30"},
		{90 * time.Second, "1:30"},
		{5 * time.Minute, "5:00"},
		{3600 * time.Second, "1:00:00"},
		{3661 * time.Second, "1:01:01"},
		{-5 * time.Second, "0:00"}, // negative clamped to 0
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestChannelList_paginationNotInContent(t *testing.T) {
	m := newTestModel(t)
	many := make([]audioaddict.Channel, 50)
	for i := range many {
		many[i] = audioaddict.Channel{ID: int64(i + 1), Key: "ch" + string(rune('a'+i%26)), Name: "Channel"}
	}
	m.channels = many
	out := renderChannels(m, 80, 30)
	if strings.Contains(out, "PgDn pages") {
		t.Errorf("pagination hint should NOT say 'PgDn pages' in content pane; got:\n%s", out)
	}
}
