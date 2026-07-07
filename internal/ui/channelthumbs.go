package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/fanart"
)

// kickoffVisibleThumbs returns Cmds to fetch thumbnails for every channel
// currently visible in the card viewport that doesn't already have one
// cached or in flight. Called from:
//
//   - handleDomain on channelsLoadedMsg  (initial paint)
//   - updateHome on Up/Down/PgUp/PgDn   (scroll exposes new cards)
//
// Returns nil when fanart is disabled (mode == None) so we don't issue
// network calls the user will never see the output of. The active mode
// (Kitty vs ASCII) drives the renderer choice per Cmd — Kitty uses the
// Unicode-placeholder protocol so images compose inside lipgloss layouts.
func (m Model) kickoffVisibleThumbs() []tea.Cmd {
	mode := fanart.DetectMode()
	if mode == fanart.ModeNone {
		return nil
	}
	vis := m.visibleChannels()
	if len(vis) == 0 {
		return nil
	}
	start, end := m.cardViewport(len(vis))
	// Prefetch margin around the visible window.
	//
	// DIMM-419 reports cards staying blank after a fast hold-Down
	// scroll. Controlled reproduction (unit + teatest with httptest
	// CDN) shows dispatch is correct at ±1 — every visited card has a
	// fetch fired. So the observed symptom must come from network-
	// side effects the in-memory tests don't model: real-CDN rate
	// limits, slow responses, HTTP/1.1 connection-per-host caps that
	// queue 25 concurrent requests behind 2 connections.
	//
	// ±4 is defense-in-depth: it widens the in-flight queue so the
	// network has more time to drain before the user outruns it, and
	// it makes the scroll-back path warmer (cards behind the current
	// position are already cached or in-flight). Cost is ~6 extra
	// HTTP requests at startup vs the previous ~3.
	const prefetch = 4
	if start-prefetch >= 0 {
		start -= prefetch
	} else {
		start = 0
	}
	if end+prefetch <= len(vis) {
		end += prefetch
	} else {
		end = len(vis)
	}

	var cmds []tea.Cmd
	for i := start; i < end; i++ {
		ch := vis[i]
		if _, exists := m.channelThumbs[ch.Key]; exists {
			continue // already cached or in flight
		}
		// Mark as in-flight by inserting an empty string. The reply
		// handler either overwrites with a real escape or deletes the
		// key so a subsequent kickoff can retry.
		m.channelThumbs[ch.Key] = ""
		cmds = append(cmds, fetchChannelThumbCmd(
			m.ctx, ch.Key, channelThumbURL(ch),
			cardThumbCols, cardThumbRows, mode, channelKittyID(ch)))
	}
	return cmds
}

// channelKittyID returns a stable Kitty image id for one channel's card
// thumbnail. Each visible card needs a UNIQUE id — Kitty's Unicode-
// placeholder protocol stores one image per id, and two cards with the
// same id would render the same image. Reserved ranges:
//
//   - 1: nowPlayingFanartID (album cover in the Now Playing pane)
//   - 100+: per-card thumbnails, keyed off channel.ID
//
// We use channel.ID + 100 because AudioAddict channel IDs are small
// integers (<10k) that fit comfortably in the 24-bit space the
// placeholder FG-color encoding gives us.
func channelKittyID(ch audioaddict.Channel) uint32 {
	return uint32(100 + ch.ID&0xFFFFFF)
}

// cardViewport returns the [start, end) indices of cards currently
// rendered, mirroring the centering logic in renderChannels. Computed off
// the model state so kickoffVisibleThumbs doesn't have to know about
// pane geometry.
func (m Model) cardViewport(total int) (int, int) {
	if total == 0 {
		return 0, 0
	}
	cardsPerView := m.cardsPerView()
	if cardsPerView < 1 {
		cardsPerView = 1
	}
	start := m.selIdx - cardsPerView/2
	if start < 0 {
		start = 0
	}
	end := start + cardsPerView
	if end > total {
		end = total
		start = end - cardsPerView
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

// cardsPerView mirrors the bodyH math in renderChannels so the prefetch
// window matches what's actually drawn.
func (m Model) cardsPerView() int {
	innerH := m.height - 5     // header + status + hints
	bodyH := innerH - 2 - 4    // pane border + (header line + separator)
	if bodyH < cardHeight {
		return 1
	}
	return bodyH / cardHeight
}

// channelThumbURL picks the best channel-art URL for the small card-
// thumb footprint and resolves the AudioAddict template to a fetchable
// URL with a size hint. Square is preferred because the swatch is
// (roughly) square; falls back to default / vertical / legacy asset_url.
func channelThumbURL(ch audioaddict.Channel) string {
	if tmpl := ch.Image.PreferredFanartURL(); tmpl != "" {
		// Request ~80px so the server returns something close to the
		// final pixel footprint (the local downsampler then targets
		// cardThumbCols × cardThumbRows*2 pixels exactly).
		return audioaddict.ResolveImageURL(tmpl, 80, 80, 75)
	}
	if ch.AssetURL != "" {
		return audioaddict.ResolveImageURL(ch.AssetURL, 80, 80, 75)
	}
	return ""
}

// Card thumbnail cell footprint. 30 cols × 15 rows of cells renders as a
// visually-square 30×30 pixel image because the half-block (▀) trick packs
// 2 vertical pixels per cell. Matches the Now Playing column's geometry
// (nowplaying.go: fanartCols=30, fanartRows=15) so both art surfaces are
// the same effective resolution.
//
// Trade-off: cards grow to 17 rows tall (15 thumb + 2 border) — at a 30-
// row terminal that's ~1–2 cards visible per screen. Scroll/PgDn become
// the primary navigation tools.
const (
	cardThumbCols = 16
	cardThumbRows = 8
)
