package ui

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/player"
)

// TestSnapshot_homeFrame writes a clean (escape-stripped) text rendering
// of the home screen to /tmp/addiplay-home.txt so the layout can be eyeballed
// without a live terminal. Excluded from the default test plan only when
// the file is removed; otherwise harmless (passes whatever the layout).
func TestSnapshot_homeFrame(t *testing.T) {
	m := newTestModel(t)
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "classictrance", Name: "Classic Trance", DescriptionShort: "the classics that built the genre"},
		{ID: 2, Key: "vocaltrance", Name: "Vocal Trance", DescriptionShort: "female vocals, soaring melodies"},
		{ID: 3, Key: "progressive", Name: "Progressive", DescriptionShort: "deep, hypnotic, sun-drenched grooves"},
		{ID: 4, Key: "trance", Name: "Trance", DescriptionShort: "uplifting trance anthems"},
	}
	m.currentChannel = "classictrance"
	m.currentNetwork = "di"
	m.playingNetwork = "di"
	m.playerSt = player.StatePlaying
	m.currentTrack = audioaddict.Track{Artist: "FilterFunk", Title: "S.O.S. (Message In A Bottle)"}
	m.cfg.AddFavorite("di", "progressive")
	m.selIdx = 0

	view := m.View()
	clean := stripANSI(view)
	if err := os.WriteFile("/tmp/addiplay-home.txt", []byte(clean), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d lines to /tmp/addiplay-home.txt", strings.Count(clean, "\n"))
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }
