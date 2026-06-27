package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
)

// updateNetworkPicker handles keys for the centered network-switch overlay.
// Picking emits networkSwitchedMsg, which the root model handles by re-
// themeing the UI and reloading channels for the chosen network.
func (m Model) updateNetworkPicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Quit), msg.Type == tea.KeyEsc:
		m.focus = FocusChannels
	case key.Matches(msg, keys.Up):
		if m.netCursor > 0 {
			m.netCursor--
		}
	case key.Matches(msg, keys.Down):
		if m.netCursor < len(audioaddict.Networks)-1 {
			m.netCursor++
		}
	case key.Matches(msg, keys.Play):
		picked := audioaddict.Networks[m.netCursor]
		m.focus = FocusChannels
		return m, switchNetworkCmd(picked.Slug)
	}
	return m, nil
}

// viewNetworkPicker renders the centered popover. Each row shows the
// network's accent color as a small swatch so the chosen theme is
// previewable before commit.
func (m Model) viewNetworkPicker() string {
	var rows []string
	rows = append(rows, m.st.header.Bold(true).Render("Switch network"))
	rows = append(rows, m.st.muted.Render(strings.Repeat("─", 30)))
	for i, n := range audioaddict.Networks {
		marker := "  "
		if i == m.netCursor {
			marker = "▸ "
		}
		theme := ThemeFor(n.Slug)
		swatch := lipgloss.NewStyle().Background(theme.Accent).Render("  ")
		line := fmt.Sprintf("%s%s  %s", marker, swatch, n.Display)
		style := lipgloss.NewStyle().Foreground(m.theme.FG)
		if i == m.netCursor {
			style = style.Bold(true).Foreground(m.theme.Accent)
		}
		rows = append(rows, style.Render(line))
	}
	rows = append(rows, "")
	rows = append(rows, m.st.keyHint.Render("↑↓ select   enter switch   esc cancel"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent).
		Padding(1, 2).
		Render(strings.Join(rows, "\n"))
	return m.st.app.Width(m.width).Height(m.height).Render(
		lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box),
	)
}
