package ui

import (
	"fmt"
	"testing"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
)

// TestKickoffVisibleThumbs_fastScroll_dispatchesForEveryVisitedCard
// reproduces DIMM-419: when the user holds Down and rips through many
// cards, every card we passed through should have a fetch dispatched
// (each becomes an in-flight entry "" or a completed escape). Cards
// that were never even iterated by kickoff — and therefore never had a
// fetch fired — are the bug.
//
// Simulation: 1 card visible at a time (cardsPerView=1), 30 channels,
// 25 simulated Down keypresses. Per-frame we call kickoffVisibleThumbs
// (the same function the Down handler calls in screen_home.go:59) and
// let it populate m.channelThumbs.
//
// Expected (post-fix): every channel index from selIdx=0 outward
// through selIdx=25 has an entry in m.channelThumbs by the end.
func TestKickoffVisibleThumbs_fastScroll_dispatchesForEveryVisitedCard(t *testing.T) {
	t.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	t.Setenv("COLORTERM", "truecolor")

	m := newTestModel(t)
	// Tight viewport: cardsPerView = bodyH / cardHeight, where
	// bodyH = (m.height-5) - 2 - 4 = m.height - 11. cardHeight = 10.
	// Set m.height=18 → bodyH=7 → branch returns cardsPerView=1.
	m.height = 18
	m.width = 80

	// 30 channels with valid image templates so channelThumbURL returns
	// non-empty; otherwise fetchChannelThumbCmd short-circuits to an
	// empty-escape reply that wouldn't tell us about dispatch behavior.
	const total = 30
	channels := make([]audioaddict.Channel, total)
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
	m.channels = channels
	m.channelThumbs = map[string]string{}

	// Initial kickoff (mirrors handleDomain on channelsLoadedMsg).
	_ = m.kickoffVisibleThumbs()

	// Hold Down for 25 keystrokes.
	const presses = 25
	for i := 0; i < presses; i++ {
		if m.selIdx < len(m.visibleChannels())-1 {
			m.selIdx++
		}
		_ = m.kickoffVisibleThumbs()
	}

	// Every channel from index 0 through selIdx should have a fetch
	// dispatched — that is, there should be an entry in channelThumbs
	// (either in-flight "" or a completed escape).
	var missing []string
	for i := 0; i <= m.selIdx; i++ {
		if _, ok := m.channelThumbs[channels[i].Key]; !ok {
			missing = append(missing, channels[i].Key)
		}
	}
	if len(missing) > 0 {
		t.Errorf("after fast-scrolling to selIdx=%d, %d cards had NO fetch dispatched: %v",
			m.selIdx, len(missing), missing)
	}
}
