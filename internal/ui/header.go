package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderHeader draws the top bar: app name + network badge + tabs (left
// cluster), the logged-in user's email + key hints (right cluster),
// padded to exactly m.width columns.
func renderHeader(m Model) string {
	app := m.st.header.Bold(true).Render("addiplay")
	badge := m.st.accentBlock.Render(m.theme.Display)

	tabFavs := m.st.tabInactive.Render("FAVORITES")
	tabAll := m.st.tabInactive.Render("ALL")
	switch m.tab {
	case TabAll:
		tabAll = m.st.tabActive.Render("◆ ALL ◆")
	case TabFavorites:
		tabFavs = m.st.tabActive.Render("◆ FAVORITES ◆")
	}
	tabs := lipgloss.JoinHorizontal(lipgloss.Top, tabAll, "  ", tabFavs)

	user := m.st.muted.Render("👤 " + m.creds.Email)
	hints := m.st.keyHint.Render("[n] network   [/] filter   [?] keys   [q] quit")
	rightCluster := lipgloss.JoinHorizontal(lipgloss.Top, user, "   ", hints)

	leftCluster := lipgloss.JoinHorizontal(lipgloss.Top, app, " ", badge, "   ", tabs)
	gap := m.width - lipgloss.Width(leftCluster) - lipgloss.Width(rightCluster)
	if gap < 1 {
		gap = 1
	}
	row := leftCluster + strings.Repeat(" ", gap) + rightCluster
	return m.st.app.Render(row) // no .Width — row is already exactly m.width
}
