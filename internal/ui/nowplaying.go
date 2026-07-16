package ui

import (
	"fmt"
	"strings"
	"time"

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
	fanartRows = 14
	// artColumnWidth = pane border (2) + pane padding (2) + image border
	// (2) + image (fanartCols) + 1 cell of room on EACH side so
	// PlaceHorizontal can visibly centre the bordered image inside the
	// pane content area.
	artColumnWidth = fanartCols + 8

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
// (top to bottom) album art, track info with vote count, progress bar,
// channel info with director and description, mode/quality badge, and
// recent track history. Always exactly w cols / h rows so it composes
// cleanly with the channel list to its left.
func (m Model) renderNowPlaying(w, h int) string {
	paneInnerW := w - 2
	pane := m.st.paneFocused.
		Padding(1, 1).
		Width(paneInnerW).
		Height(h - 2)

	contentW := paneInnerW - 2 // padding inside pane

	// --- Image block ---
	image := m.fanartEscape
	if image == "" {
		image = fanart.Placeholder(fanartCols, fanartRows/2,
			string(m.theme.BGAlt), string(m.theme.Accent))
	}
	imageBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(m.theme.FGMuted).
		Render(image)
	imageBox = lipgloss.PlaceHorizontal(contentW, lipgloss.Center, imageBox)

	rows := []string{imageBox, ""}

	// --- Track info ---
	switch {
	case m.playerSt == player.StateError:
		rows = append(rows, m.st.toast.Render(" stream error "))
	case m.resolving || m.playerSt == player.StateLoading:
		rows = append(rows, m.st.muted.Render("loading…"))
	case m.currentTrack.Artist != "" || m.currentTrack.Title != "":
		titleStr := m.st.nowPlaying.Bold(true).Render(
			truncateLine(m.currentTrack.Title, contentW-6) + heartGlyph(m))
		voteStr := ""
		if m.voteUp > 0 {
			voteStr = m.st.muted.Render(fmt.Sprintf("▲ %d", m.voteUp))
		}
		if voteStr != "" {
			rows = append(rows, padRowTo(titleStr, voteStr, contentW))
		} else {
			rows = append(rows, titleStr)
		}
		rows = append(rows, m.st.muted.Padding(0, 1).Render(m.currentTrack.Artist))
	case m.currentTrack.Track != "":
		rows = append(rows, m.st.nowPlaying.Render(
			truncateLine(m.currentTrack.Track, contentW-4)+heartGlyph(m)))
	default:
		rows = append(rows, m.st.muted.Padding(0, 1).Render("(no track info)"))
	}

	// --- Progress bar ---
	if m.playerSt == player.StatePlaying || m.playerSt == player.StatePaused {
		rows = append(rows, "")
		rows = append(rows, m.renderProgressBar(contentW))
	}

	rows = append(rows, "")

	// --- Channel info ---
	ch := m.playingChannel()
	rows = append(rows, lipgloss.NewStyle().Foreground(m.theme.FG).Bold(true).Render(channelLabel(m)))

	if ch.ChannelDirector != "" {
		rows = append(rows, m.st.muted.Render("curated by "+ch.ChannelDirector))
	}

	// Description — word-wrapped, up to 4 lines.
	desc := firstNonEmptyStr(ch.Description, ch.DescriptionLong, ch.DescriptionShort)
	if desc != "" {
		wrapped := lipgloss.NewStyle().Width(contentW).Render(
			m.st.muted.Render(desc))
		lines := strings.Split(wrapped, "\n")
		if len(lines) > 4 {
			lines = lines[:4]
		}
		rows = append(rows, strings.Join(lines, "\n"))
	}

	rows = append(rows, "")

	// --- Mode / quality / queue position badge ---
	badges := m.renderBadges()
	if badges != "" {
		rows = append(rows, m.st.muted.Render(badges))
	}

	// --- Recent tracks ---
	if len(m.recentTracks) > 0 {
		rows = append(rows, "")
		rows = append(rows, m.st.muted.Render("recently on this channel:"))
		maxTracks := 5
		if len(m.recentTracks) < maxTracks {
			maxTracks = len(m.recentTracks)
		}
		for i := 0; i < maxTracks; i++ {
			rt := m.recentTracks[i]
			line := truncateLine("· "+rt.Track, contentW)
			rows = append(rows, m.st.muted.Render(line))
		}
	}

	return pane.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// playingChannel returns the Channel struct for the currently-playing channel.
func (m Model) playingChannel() audioaddict.Channel {
	for _, ch := range m.channels {
		if ch.Key == m.currentChannel {
			return ch
		}
	}
	return audioaddict.Channel{}
}

// renderProgressBar draws a horizontal bar with elapsed/duration time.
// In live mode (duration unknown), shows only elapsed time.
func (m Model) renderProgressBar(w int) string {
	var elapsed time.Duration
	switch m.playerSt {
	case player.StatePlaying:
		if !m.trackStartTime.IsZero() {
			elapsed = time.Since(m.trackStartTime)
		}
	case player.StatePaused:
		elapsed = m.trackPauseElapsed
	default:
		return ""
	}

	dur := time.Duration(m.currentTrack.Duration) * time.Second

	if dur > 0 {
		timeStr := fmt.Sprintf(" %s / %s", formatDuration(elapsed), formatDuration(dur))
		barW := w - lipgloss.Width(timeStr)
		if barW < 5 {
			return m.st.muted.Render(timeStr)
		}
		frac := float64(elapsed) / float64(dur)
		if frac > 1 {
			frac = 1
		}
		if frac < 0 {
			frac = 0
		}
		filled := int(frac * float64(barW))
		empty := barW - filled
		filledBar := lipgloss.NewStyle().Foreground(m.theme.Accent).Render(strings.Repeat("━", filled))
		emptyBar := m.st.muted.Render(strings.Repeat("─", empty))
		timePart := m.st.muted.Render(timeStr)
		return filledBar + emptyBar + timePart
	}

	return m.st.muted.Render(formatDuration(elapsed))
}

// renderBadges returns the mode/quality/queue-position line.
func (m Model) renderBadges() string {
	var parts []string
	if m.trackQueue != nil {
		parts = append(parts, "on-demand")
	} else if m.currentChannel != "" {
		parts = append(parts, "live")
	}
	if m.trackQueue != nil {
		pos, total := m.trackQueue.Position()
		if total > 0 {
			parts = append(parts, fmt.Sprintf("track %d of %d", pos, total))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Truncate(time.Second)
	total := int(d.Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func firstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
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
