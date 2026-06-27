package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/player"
)

// renderStatus draws the player horizontal bar at the bottom — two rows
// sharing the same background so they read as one cohesive bar:
//
//	row 1: ⏵ station · track — artist                          vol 65%
//	row 2: [space] pause   [enter] play   [f] ★   [/] filter   [n] network   [q] quit
//
// Optional toast row stacks above when there's a transient error.
func renderStatus(m Model) string {
	// statusBar style has Padding(0,1), which lipgloss adds OUTSIDE Width,
	// so the inner content must be (m.width - 2) to avoid wrap.
	innerNow := padTo(m.width-2, nowPlayingText(m), volumeText(m))
	statusLine := m.st.statusBar.Render(innerNow)

	// Hints row uses the SAME statusBar style for a unified bar look; the
	// text is muted so it visually recedes vs the now-playing line above.
	innerHints := padTo(m.width-2, m.st.muted.Render(hintsText(m)), "")
	hintLine := m.st.statusBar.Render(innerHints)

	if m.toast != "" {
		toast := m.st.toast.Width(m.width).Render(" ! " + m.toast)
		return lipgloss.JoinVertical(lipgloss.Left, toast, statusLine, hintLine)
	}
	return lipgloss.JoinVertical(lipgloss.Left, statusLine, hintLine)
}

// nowPlayingText is the left half of the status line.
func nowPlayingText(m Model) string {
	glyph := playerGlyph(m.playerSt)
	if m.resolving {
		glyph = "◐"
	}
	if m.currentChannel == "" {
		star := ""
		if ch, ok := m.selectedChannel(); ok && m.cfg.IsFavorite(m.currentNetwork, ch.Key) {
			star = m.st.star.Render(" ★")
		}
		hint := "select a channel · [enter] to play"
		if m.resolving {
			hint = "resolving stream…"
		}
		return fmt.Sprintf("%s  %s%s", glyph, hint, star)
	}
	label := channelLabel(m)
	// When the user has switched the browsing network but playback
	// continues from a different network, prefix the label so the
	// mismatch is visible (e.g. "RockRadio · 80s Rock · Bon Jovi —…"
	// while the channel list pane shows DI.fm).
	if m.playingNetwork != "" && m.playingNetwork != m.currentNetwork {
		if n, ok := audioaddict.NetworkBySlug(m.playingNetwork); ok {
			label = n.Display + " · " + label
		}
	}
	star := ""
	if m.cfg.IsFavorite(m.playingNetwork, m.currentChannel) {
		star = m.st.star.Render(" ★")
	}
	track := strings.TrimSpace(m.currentTrack.Track)
	if track == "" {
		return fmt.Sprintf("%s  %s%s · —", glyph, label, star)
	}
	return fmt.Sprintf("%s  %s%s · %s", glyph, label, star, track)
}

// volumeText is the right half of the status line.
func volumeText(m Model) string {
	return fmt.Sprintf("vol %d%%", m.cfg.Volume)
}

// hintsText is the second bar line — context-sensitive keybinding hints.
func hintsText(m Model) string {
	switch m.focus {
	case FocusSearch:
		return "[esc] close   [enter] keep filter"
	case FocusNetworkPicker:
		return "[↑↓] move   [enter] switch   [esc] cancel"
	}
	return "[space] pause   [enter] play   [f] favorite   [/] filter   [n] network   [L] logout   [q] quit"
}

// playerGlyph returns the leading status glyph for the given player state.
func playerGlyph(s player.State) string {
	switch s {
	case player.StatePlaying:
		return "⏵"
	case player.StatePaused:
		return "⏸"
	case player.StateLoading:
		return "◐"
	case player.StateError:
		return "✗"
	default:
		return "·"
	}
}

// padTo joins left and right with enough spaces to fill width.
func padTo(width int, left, right string) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right + " "
}

// channelLabel returns the human-readable name of the currently-playing
// channel, falling back to the slug if the channel list isn't loaded yet.
func channelLabel(m Model) string {
	for _, ch := range m.channels {
		if ch.Key == m.currentChannel {
			return ch.Name
		}
	}
	return m.currentChannel
}
