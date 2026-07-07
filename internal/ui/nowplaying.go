package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/fanart"
	"github.com/dimmkirr/addiplay/internal/player"
)

// Fanart cell geometry. Two profiles based on the active fanart mode:
//
//   - Kitty graphics: real pixels at the image's resolution, so a small
//     cell footprint is fine (the terminal scales pixels into cells).
//   - ASCII half-block: each cell shows 1×2 pixels via the ▀ trick;
//     fidelity scales with cell count, so use ~3× the cells.
//
// Frame is square (album-cover aspect). Both AudioAddict track art_url and
// channels.images.square are 1:1, so a square frame lets either source fit
// without crop or letterbox.
const (
	// Now Playing geometry — ONE footprint for both Kitty and ASCII so
	// the right pane stays the same width regardless of fanart mode.
	// Previously ASCII used 50×25 cells while Kitty used 30×15, which
	// shifted the entire layout when the terminal switched mode (e.g.
	// entering tmux dropped Kitty → ASCII and grew the pane from 36 to
	// 56 cells, eating space from the channel list). User feedback
	// (image 12 preferred over image 10): pick the smaller Kitty
	// dimensions everywhere; ASCII renders the same physical screen
	// area, just at lower fidelity.
	fanartCols = 30
	fanartRows = 15
	// artColumnWidth = pane border (2) + image border (2) + image
	// (fanartCols) + 1 cell of room on EACH side so PlaceHorizontal
	// can visibly centre the bordered image inside the pane content
	// area. Without the extra 2 cells the bordered image fills the
	// pane edge-to-edge and looks left-biased.
	artColumnWidth = fanartCols + 6

	minWidthForArt = 100 // below this, drop the art column entirely

	// nowPlayingFanartID is the STABLE Kitty image id used for the Now
	// Playing pane. Per the Kitty graphics protocol spec, transmitting
	// new image bytes with the same `i=N` REPLACES the stored image and
	// auto-updates any visible placement showing it. Using different
	// IDs per URL (the previous behaviour) created independent placements
	// that stacked — which is why the channel art kept rendering on top
	// of the track album cover when a song with `art_url` started.
	//
	// Per-card thumbnails use ASCII (not Kitty) so this id namespace is
	// private to the Now Playing pane.
	nowPlayingFanartID uint32 = 1
)

// fanartDimensions returns the cell footprint for the Now Playing pane.
// Identical for Kitty and ASCII so the layout stays stable when the
// terminal mode flips (e.g. user moves to/from tmux without passthrough).
// The mode parameter is retained for API stability and possible future
// per-mode tuning.
func fanartDimensions(_ fanart.Mode) (cols, rows, columnWidth int) {
	return fanartCols, fanartRows, artColumnWidth
}

// preferredFanartSource picks the URL that best represents what's currently
// playing. Per-track album cover (track_history.art_url) wins when present
// because it changes with each song; channel art is the fallback.
func preferredFanartSource(track audioaddict.Track, ch audioaddict.Channel, mode fanart.Mode) string {
	cols, rows, _ := fanartDimensions(mode)
	w, h := cols*10, rows*20
	if track.ArtURL != "" {
		return audioaddict.ResolveImageURL(track.ArtURL, w, h, 75)
	}
	if tmpl := ch.Image.PreferredFanartURL(); tmpl != "" {
		return audioaddict.ResolveImageURL(tmpl, w, h, 75)
	}
	if ch.AssetURL != "" {
		return audioaddict.ResolveImageURL(ch.AssetURL, w, h, 75)
	}
	return ""
}

// refreshFanart resolves the desired fanart source for (track, channel)
// and either paints the cached escape or returns a fetch command.
//
// IMPORTANT: while a new fetch is in flight we DELIBERATELY leave any
// previously-rendered escape in place rather than clearing it to the
// placeholder. Reason: under the typical stream flow we call refreshFanart
// twice in quick succession —
//
//	streamPlayingMsg     →  refreshFanart({}, ch)      → channel art
//	trackUpdateMsg (~150 ms later) → refreshFanart(track, ch) → album art
//
// Clearing fanartEscape on the second call produced the "channel art
// flashes then disappears" symptom the user reported (placeholder shown
// for the 200–500 ms while the album fetch runs). Keeping the stale
// escape means the user always sees SOMETHING: channel art while the
// album loads, and — if the album fetch fails — channel art permanently
// instead of an empty pane.
func (m *Model) refreshFanart(track audioaddict.Track, ch audioaddict.Channel) tea.Cmd {
	mode := fanart.DetectMode()
	dlog("refreshFanart: mode=%s track.ArtURL=%q ch.Key=%q ch.AssetURL=%q",
		mode, track.ArtURL, ch.Key, ch.AssetURL)
	if mode == fanart.ModeNone {
		m.fanartEscape = ""
		m.fanartSourceURL = ""
		return nil
	}
	src := preferredFanartSource(track, ch, mode)
	if src == "" {
		dlog("refreshFanart: no source URL — clearing")
		m.fanartEscape = ""
		m.fanartSourceURL = ""
		return nil
	}
	dlog("refreshFanart: src=%q currentSrc=%q hasEscape=%t", src, m.fanartSourceURL, m.fanartEscape != "")
	if src == m.fanartSourceURL && m.fanartEscape != "" {
		return nil // already showing this exact art
	}
	// Channel-switch clear (DIMM-420 #1): when streamPlayingMsg fires on
	// a different channel it calls refreshFanart with an empty Track. If
	// we leave the previous channel/track's escape in place AND the new
	// fetch fails, the user keeps seeing the WRONG channel's art forever.
	// Detect this case (empty track + changing source) and drop the stale
	// escape so a failed fetch degrades to the placeholder.
	//
	// Track-update within the same channel (track is NOT empty) still
	// keeps the prior escape so the user never sees a placeholder flash
	// when a new song begins — that's the no-flash invariant locked by
	// TestRefreshFanart_doesNotClearStaleEscapeMidLoad.
	if track.ID == 0 && track.ArtURL == "" {
		m.fanartEscape = ""
	}
	m.fanartSourceURL = src
	// Stable Kitty image id (see comment on nowPlayingFanartID). DO NOT
	// switch to a URL-derived id — it re-introduces the stacked-placement
	// bug where the channel art persists under the album art.
	m.fanartID = nowPlayingFanartID
	if cached := m.fanartCache.Get(src); cached != "" {
		m.fanartEscape = cached
		return nil
	}
	// NOTE: do NOT clear m.fanartEscape here. See the comment block above.
	cols, rows, _ := fanartDimensions(mode)
	return fetchFanartCmd(m.ctx, m.fanartCache, src, cols, rows, m.fanartID, mode)
}

// renderNowPlaying draws the right-side column: bordered panel containing
// (top to bottom) the channel artwork (or placeholder), channel name +
// tagline, and the current track artist/title. Always exactly w cols /
// h rows so it composes cleanly with the channel list to its left.
//
// Every section has a fallback so a missing field never produces a totally
// blank pane — the user always has SOME context about what's playing.
func (m Model) renderNowPlaying(w, h int) string {
	paneInnerW := w - 2 // pane border eats 2 cells
	pane := m.st.paneFocused.Width(paneInnerW).Height(h - 2)

	// Image block — fanart bytes (Unicode placeholders w/ diacritics in
	// Kitty mode; half-block art in ASCII) if loaded, otherwise a coloured
	// placeholder so the pane geometry stays stable across both states.
	image := m.fanartEscape
	if image == "" {
		dlog("renderNowPlaying: no fanartEscape — using placeholder (w=%d h=%d)", w, h)
		image = fanart.Placeholder(fanartCols, fanartRows/2,
			string(m.theme.BGAlt), string(m.theme.Accent))
	} else {
		dlog("renderNowPlaying: painting fanartEscape len=%d (w=%d h=%d)", len(image), w, h)
	}
	// Wrap in a thin outline so the album cover reads as a distinct
	// container instead of bleeding into the rest of the pane. The
	// border characters are normal text drawn by lipgloss; Kitty
	// placeholders inside the border still encode their cell coords
	// via diacritics, so the image renders at the bordered placeholder
	// positions (1 cell right + 1 cell down from where the unbordered
	// image used to land — that's the outline's interior space).
	imageBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(m.theme.FGMuted).
		Render(image)
	// Centre horizontally within the pane. With artColumnWidth =
	// fanartCols + 6 the bordered image is 2 cells narrower than the
	// pane content, leaving 1 cell of room on each side after centring.
	imageBox = lipgloss.PlaceHorizontal(paneInnerW, lipgloss.Center, imageBox)

	label := m.st.nowPlaying.Bold(true).Render(channelLabel(m))

	// Channel tagline (description_short) when available.
	tagline := ""
	for _, ch := range m.channels {
		if ch.Key == m.currentChannel && ch.DescriptionShort != "" {
			tagline = m.st.muted.Padding(0, 1).Render(truncateLine(ch.DescriptionShort, w-4))
			break
		}
	}

	// Track block — pick the most informative form available.
	trackBlock := ""
	switch {
	case m.playerSt == player.StateError:
		trackBlock = m.st.toast.Render(" stream error ")
	case m.resolving || m.playerSt == player.StateLoading:
		trackBlock = m.st.muted.Render("loading…")
	case m.currentTrack.Artist != "" || m.currentTrack.Title != "":
		trackBlock = lipgloss.JoinVertical(lipgloss.Left,
			m.st.nowPlaying.Bold(true).Render(m.currentTrack.Artist+heartGlyph(m)),
			m.st.muted.Padding(0, 1).Render(m.currentTrack.Title),
		)
	case m.currentTrack.Track != "":
		trackBlock = m.st.nowPlaying.Render(m.currentTrack.Track + heartGlyph(m))
	default:
		trackBlock = m.st.muted.Padding(0, 1).Render("(no track info)")
	}

	// Two blank rows below the bordered image give the album cover
	// breathing room before the channel label / track block start.
	rows := []string{imageBox, "", "", label}
	if tagline != "" {
		rows = append(rows, tagline)
	}
	rows = append(rows, trackBlock)
	return pane.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// heartGlyph returns the leading-space-prefixed vote indicator for the
// currently-playing track. The three glyphs come from a single
// `IconSet` (see icons.go) so they render at consistent metrics
// regardless of vote state — historically we used U+2665 / U+2661 /
// U+2298 which were sourced from three different fallback fonts and
// jittered visibly when the user toggled the vote. Default set is
// Nerd Font; users without one can `export ADDIPLAY_ICONS=unicode`.
//
// Empty when there is no real track (ID == 0 — ad break / show
// without ID). Liked and disliked are mutually exclusive (DIMM-382);
// if both flags ever desync, disliked wins in render so the user
// sees the more recent action.
func heartGlyph(m Model) string {
	if m.currentTrack.ID == 0 {
		return ""
	}
	icons := Icons()
	if m.dislikedTracks[m.currentTrack.ID] {
		// Inline error-color style — the `toast` style has padding +
		// background and would render as a chip instead of a glyph.
		return " " + lipgloss.NewStyle().Foreground(m.theme.Error).Render(icons.HeartBroken)
	}
	if m.likedTracks[m.currentTrack.ID] {
		return " " + m.st.star.Render(icons.HeartFilled)
	}
	return " " + m.st.muted.Render(icons.HeartOutline)
}

// truncateLine clamps s to maxWidth visible columns, appending "…" if cut.
func truncateLine(s string, maxWidth int) string {
	if maxWidth <= 1 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxWidth {
		return s
	}
	return string(r[:maxWidth-1]) + "…"
}
