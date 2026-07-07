package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
)

// Small pure helpers shared across screens. Kept in one file so they're
// trivially discoverable rather than scattered across screen_*.go.

// renderCenteredPopover wraps content in a rounded-border box (accent
// border, padded) and centers it on a full-screen canvas sized to
// (m.width, m.height). Shared between viewLogin and viewNetworkPicker
// so the two overlays look identical and any styling change happens
// in one place. padX/padY are the inner padding inside the box.
func (m Model) renderCenteredPopover(content string, padX, padY int) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent).
		Padding(padY, padX).
		Render(content)
	return m.st.app.Width(m.width).Height(m.height).Render(
		lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box),
	)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// selectedChannel returns the currently-highlighted channel of the visible list.
func (m Model) selectedChannel() (audioaddict.Channel, bool) {
	vis := m.visibleChannels()
	if m.selIdx < 0 || m.selIdx >= len(vis) {
		return audioaddict.Channel{}, false
	}
	return vis[m.selIdx], true
}

// visibleChannels applies the active tab (All / Favorites) and search
// filter to the loaded channel list.
func (m Model) visibleChannels() []audioaddict.Channel {
	src := m.channels
	if m.tab == TabFavorites {
		src = src[:0:0]
		for _, ch := range m.channels {
			if m.cfg.IsFavorite(m.currentNetwork, ch.Key) {
				src = append(src, ch)
			}
		}
	}
	q := strings.TrimSpace(strings.ToLower(m.searchInput.Value()))
	if q == "" {
		return src
	}
	out := make([]audioaddict.Channel, 0, len(src))
	for _, ch := range src {
		if strings.Contains(strings.ToLower(ch.Name), q) {
			out = append(out, ch)
		}
	}
	return out
}

func networkIdxForSlug(slug string) int {
	for i, n := range audioaddict.Networks {
		if n.Slug == slug {
			return i
		}
	}
	return 0
}

func channelIDFromKey(channels []audioaddict.Channel, key string) int64 {
	for _, ch := range channels {
		if ch.Key == key {
			return ch.ID
		}
	}
	return 0
}

func channelByID(channels []audioaddict.Channel, id int64) (audioaddict.Channel, bool) {
	for _, ch := range channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return audioaddict.Channel{}, false
}

// idFromURL was a previous attempt to derive a per-URL Kitty image id.
// REMOVED: it caused each new URL to allocate a fresh image storage slot
// and a NEW placement on screen, so old placements stacked instead of
// being replaced. The Now Playing pane now uses a stable id (see
// `nowPlayingFanartID` in nowplaying.go) so each transmission overwrites
// the prior image bytes and the single placement auto-updates.
