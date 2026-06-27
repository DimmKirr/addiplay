package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/player"
)

// Card geometry. Each card occupies cardHeight terminal rows including
// border; content area is cardHeight-2 rows tall.
//
// Height is driven by the thumbnail footprint (cardThumbRows + 2 for the
// rounded-border top/bottom). 17 rows lets us show a visually-square
// 30×30 px thumbnail beside the channel name + tagline + now-playing
// data. At a 30-row terminal that's ~1–2 cards visible per screen.
const (
	cardHeight = cardThumbRows + 2
	cardThumbW = cardThumbCols
)

// renderChannels draws the channel list — a vertical stack of 8-line cards.
// w is the total pane width; h is the total pane height (we'll subtract
// header + separator + footer rows internally).
func renderChannels(m Model, w, h int) string {
	pane := m.st.paneFocused
	if m.focus == FocusNetworkPicker || m.focus == FocusLogin {
		pane = m.st.paneBlurred
	}

	var header string
	if m.focus == FocusSearch {
		header = m.searchInput.View()
	} else {
		count := len(m.visibleChannels())
		header = m.st.muted.Render(fmt.Sprintf("%d channels — j/k move, enter play, / search, tab favs", count))
	}

	rows := []string{header, m.st.muted.Render(strings.Repeat("─", maxInt(w-4, 4)))}

	vis := m.visibleChannels()
	if len(vis) == 0 {
		empty := "no channels"
		if m.tab == TabFavorites {
			empty = "no favorites yet — press [f] on a channel"
		}
		rows = append(rows, m.st.muted.Render(empty))
		return pane.Width(w).Height(h).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	}

	// Viewport: how many full cards fit in the available body height?
	// 2 rows already consumed by header + separator; need 1 row of breathing
	// room at the bottom.
	bodyH := h - 4
	if bodyH < cardHeight {
		bodyH = cardHeight // always show at least one full card
	}
	cardsPerView := bodyH / cardHeight
	if cardsPerView < 1 {
		cardsPerView = 1
	}

	// Centered-on-selection window with edge clamping. This keeps the
	// highlighted card near the middle of the pane during navigation
	// rather than pinning it to the bottom (the prior behaviour).
	start := m.selIdx - cardsPerView/2
	if start < 0 {
		start = 0
	}
	end := start + cardsPerView
	if end > len(vis) {
		end = len(vis)
		start = end - cardsPerView
		if start < 0 {
			start = 0
		}
	}

	// Inner card width fits inside the pane border + 1col padding each side.
	cardW := w - 4
	for i := start; i < end; i++ {
		ch := vis[i]
		selected := i == m.selIdx
		playing := ch.Key == m.currentChannel && m.playingNetwork == m.currentNetwork
		rows = append(rows, renderCard(m, ch, selected, playing, cardW))
	}

	// Footer hint about hidden items so users know there's more below/above.
	if end < len(vis) || start > 0 {
		more := fmt.Sprintf("⋮ showing %d–%d of %d — PgDn pages, / searches", start+1, end, len(vis))
		rows = append(rows, m.st.muted.Render(more))
	}

	return pane.Width(w).Height(h).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// renderCard draws one 8-row channel card: thumbnail swatch on the left,
// info block on the right. Selected card gets accent border + tinted bg;
// playing card gets a "▶ playing" badge in the top-right.
//
// Data-density caveat: per-card now-playing track and listener counts
// would need a separate poll of /v1/<net>/currently_playing (one call
// returns tracks for ALL channels). Not wired yet — placeholder slots
// here go to "—" for non-playing channels.
func renderCard(m Model, ch audioaddict.Channel, selected, playing bool, width int) string {
	// Border + background per state. Selected card uses the rounded border
	// in the theme accent; everything else uses a muted normal border so
	// the cursor card visually pops.
	borderStyle := lipgloss.NormalBorder()
	borderColor := lipgloss.Color(string(m.theme.FGMuted))
	bg := lipgloss.NoColor{}
	var cardBg lipgloss.TerminalColor = bg
	if selected {
		borderStyle = lipgloss.RoundedBorder()
		borderColor = m.theme.Accent
		cardBg = m.theme.BGAlt
	}

	// Thumbnail. Prefer real ASCII art fetched from the channel's
	// images.square (channelthumbs.go owns the fetch + cache); fall back
	// to a coloured swatch while the fetch is in flight, or forever if
	// the channel has no usable image URL.
	thumb := m.channelThumbs[ch.Key]
	if thumb == "" {
		thumbColor := hashColor(ch.Key)
		if playing {
			thumbColor = m.theme.Accent
		}
		thumb = lipgloss.NewStyle().
			Background(thumbColor).
			Width(cardThumbW).
			Height(cardHeight - 2).
			Render("")
	}

	// Right-side content width: subtract border (2), padding (2), thumb (4),
	// and one column of gap between thumb and content.
	contentW := width - 2 - 2 - cardThumbW - 1
	if contentW < 10 {
		contentW = 10
	}

	// Build the 6 content rows.
	title := buildCardTitle(m, ch, selected, playing, contentW)
	tagline := buildCardTagline(m, ch, contentW)
	separator := m.st.muted.Render(strings.Repeat("─", contentW))
	npArtist, npTitle, metaLine := buildCardTrackRows(m, ch, playing, contentW)

	contentRows := []string{
		title,
		tagline,
		separator,
		npArtist,
		npTitle,
		metaLine,
	}
	// Pad the content column to match the thumbnail's row height so the
	// card border closes cleanly at the bottom and the JoinHorizontal
	// alignment isn't fighting two differently-sized children.
	for len(contentRows) < cardHeight-2 {
		contentRows = append(contentRows, "")
	}
	content := lipgloss.JoinVertical(lipgloss.Left, contentRows...)

	body := lipgloss.JoinHorizontal(lipgloss.Top, thumb, " ", content)

	cardStyle := lipgloss.NewStyle().
		Border(borderStyle).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(width)
	if selected {
		cardStyle = cardStyle.Background(cardBg)
	}
	return cardStyle.Render(body)
}

// buildCardTitle is the top line: cursor marker + name + favourite star +
// optional right-aligned "▶ playing" badge.
func buildCardTitle(m Model, ch audioaddict.Channel, selected, playing bool, w int) string {
	cursor := "  "
	nameStyle := m.st.channelRow.Padding(0)
	if selected {
		cursor = m.st.channelSel.Padding(0).Render("▸ ")
		nameStyle = nameStyle.Bold(true).Foreground(m.theme.Accent)
	}

	star := ""
	if m.cfg.IsFavorite(m.currentNetwork, ch.Key) {
		star = " " + m.st.star.Render("★")
	}

	left := cursor + nameStyle.Render(ch.Name) + star

	right := ""
	if playing {
		switch m.playerSt {
		case player.StatePlaying:
			right = lipgloss.NewStyle().Foreground(m.theme.Accent).Bold(true).Render("▶ playing")
		case player.StatePaused:
			right = m.st.muted.Render("⏸ paused")
		case player.StateLoading:
			right = m.st.muted.Render("◐ loading")
		case player.StateError:
			right = lipgloss.NewStyle().Foreground(m.theme.Error).Render("✗ error")
		}
	}
	return padRowTo(left, right, w)
}

// buildCardTagline is the second card line: description_short, truncated.
// Falls back to a muted dash when the API didn't return one.
func buildCardTagline(m Model, ch audioaddict.Channel, w int) string {
	if ch.DescriptionShort != "" {
		return m.st.muted.Render(truncateLine(ch.DescriptionShort, w))
	}
	return m.st.muted.Render("—")
}

// buildCardTrackRows is the three-row data block at the bottom of each
// card. For the currently-playing channel we have m.currentTrack filled
// in; for everything else we show muted placeholders (would need
// per-channel polling to populate — see TODO at the top of this file).
func buildCardTrackRows(m Model, ch audioaddict.Channel, playing bool, w int) (artist, title, meta string) {
	const labelPrefix = "♪ "
	if playing && (m.currentTrack.Artist != "" || m.currentTrack.Title != "") {
		artist = m.st.nowPlaying.Padding(0).Bold(true).Render(labelPrefix + m.currentTrack.Artist)
		title = m.st.muted.Render("  " + truncateLine(m.currentTrack.Title, w-2))
	} else if playing && m.currentTrack.Track != "" {
		artist = m.st.nowPlaying.Padding(0).Render(labelPrefix + truncateLine(m.currentTrack.Track, w-2))
		title = m.st.muted.Render("")
	} else if playing {
		artist = m.st.muted.Render(labelPrefix + "(no track info)")
		title = m.st.muted.Render("")
	} else {
		// TODO: per-channel now-playing requires polling /currently_playing
		// for ALL channels on the network and stashing the results in
		// Model.tracks[channelID]. Single API call, but adds plumbing.
		artist = m.st.muted.Render(labelPrefix + "—")
		title = m.st.muted.Render("")
	}
	// Metadata: bitrate + asset id; listener counts intentionally omitted
	// (AudioAddict's public API doesn't expose them).
	parts := []string{"premium_high · 256k aac"}
	if ch.AssetURL != "" {
		parts = append(parts, fmt.Sprintf("asset %d", ch.ID))
	}
	meta = m.st.muted.Render(truncateLine(strings.Join(parts, "  ·  "), w))
	return
}

// padRowTo joins left + right with enough spaces to fill exactly w cells.
// If the joined string is already wider, right is dropped and left is
// truncated.
func padRowTo(left, right string, w int) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	gap := w - lw - rw
	if gap < 1 {
		return truncateLine(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// hashColor returns a stable colour derived from a channel key. Used as a
// placeholder thumbnail tint until per-channel art is wired through the
// fanart pipeline. Deliberately picks from a muted-saturation palette so
// it harmonises with the dark theme background.
func hashColor(s string) lipgloss.Color {
	palette := []lipgloss.Color{
		"#3a4a6b", "#5a3a6b", "#6b3a4a", "#6b5a3a",
		"#3a6b4a", "#3a6b6b", "#4a6b3a", "#6b3a3a",
	}
	const offset, prime uint32 = 2166136261, 16777619
	h := offset
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return palette[int(h%uint32(len(palette)))]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
