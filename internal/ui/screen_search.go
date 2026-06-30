package ui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// updateSearch handles keys while the / filter input is focused.
// Esc/Enter exits back to FocusChannels; everything else is forwarded
// to the textinput and triggers a re-filter via visibleChannels().
//
// Lives in its own file because search is conceptually a distinct screen
// mode (transient overlay over the channel list); keeping its key handler
// separate makes the active focus mode obvious from the file layout.
func (m Model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyEnter:
		m.focus = FocusChannels
		m.searchInput.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	m.selIdx = 0
	return m, cmd
}
