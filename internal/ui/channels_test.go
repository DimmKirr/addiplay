package ui

import (
	"strings"
	"testing"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
)

// TestRenderCard_omitsCodecJargon enforces DIMM-395: the channel card must
// NOT carry developer-internal codec/quality data ("premium_high · 256k aac"
// was previously hard-coded onto every card with no per-channel meaning).
// Card real estate should describe the channel and the music, not how we
// happen to be streaming it.
//
// If we want the codec visible, it goes in the bottom status bar (where
// the wireframe `docs/wireframes/play.png` originally placed it), not
// on every channel card.
func TestRenderCard_omitsCodecJargon(t *testing.T) {
	m := newTestModel(t)
	m.channels = []audioaddict.Channel{
		{ID: 1, Key: "trance", Name: "Trance", AssetURL: "https://example/asset.png"},
	}

	out := renderCard(m, m.channels[0], false /*selected*/, false /*playing*/, 80)

	bannedTokens := []string{
		"premium_high",
		"256k",
		" aac", // leading space so we don't false-positive on e.g. an artist name
		"asset ",
	}
	for _, tok := range bannedTokens {
		if strings.Contains(out, tok) {
			t.Errorf("card contains %q which is dev jargon and shouldn't be on a listener-facing card:\n%s", tok, out)
		}
	}
}

// TestRenderCard_heightStableAfterMetaDrop ensures the card still fills
// its row budget after dropping the meta line. The card layout relies on
// `cardHeight-2` rows of content for the border math to close cleanly.
func TestRenderCard_heightStableAfterMetaDrop(t *testing.T) {
	m := newTestModel(t)
	m.channels = []audioaddict.Channel{{ID: 1, Key: "trance", Name: "Trance"}}
	out := renderCard(m, m.channels[0], false, false, 80)
	// renderCard returns a bordered string; total rows = top border + content + bottom border.
	rows := strings.Count(out, "\n") + 1
	if rows < cardHeight {
		t.Errorf("card rows = %d, want >= %d (cardHeight); border may have collapsed:\n%s", rows, cardHeight, out)
	}
}
