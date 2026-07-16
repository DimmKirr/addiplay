package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
)

// handleMouse processes mouse events on the home screen (FocusChannels).
//
// Layout for hit-testing (terminal Y coordinates):
//
//	row 0:      header (renderHeader)
//	row 1:      pane top border
//	row 2:      channel count / search bar
//	row 3:      separator ("───…")
//	row 4+:     cards, each cardHeight rows tall
const channelCardsStartY = 4

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && msg.Y == 0 {
		return m.handleHeaderClick(msg.X)
	}

	vis := m.visibleChannels()
	if len(vis) == 0 {
		return m, nil
	}

	switch msg.Button {
	case tea.MouseButtonWheelDown:
		if m.selIdx < len(vis)-1 {
			m.selIdx++
		}
		return m, tea.Batch(m.kickoffVisibleThumbs()...)

	case tea.MouseButtonWheelUp:
		if m.selIdx > 0 {
			m.selIdx--
		}
		return m, tea.Batch(m.kickoffVisibleThumbs()...)

	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		idx, ok := m.cardIndexFromY(msg.Y, vis)
		if !ok {
			return m, nil
		}
		if idx == m.selIdx {
			if ch, ok := m.selectedChannel(); ok && m.player != nil && !m.resolving {
				m.toast = ""
				m.toastIsWarn = false
				m.resolving = true
				return m, playSelectedCmd(m.ctx, m.client, m.player, m.currentNetwork, ch)
			}
			return m, nil
		}
		m.selIdx = idx
		return m, tea.Batch(m.kickoffVisibleThumbs()...)
	}

	return m, nil
}

// handleHeaderClick dispatches clicks on the header row (Y=0) to the
// correct action based on X coordinate. The header layout is:
//
//	app + " " + badge + "   " + tabAll + "  " + tabFavs + gap + right
func (m Model) handleHeaderClick(x int) (tea.Model, tea.Cmd) {
	appName := "addiplay"
	if m.width < 100 {
		appName = "add"
	}
	appW := lipgloss.Width(m.st.header.Bold(true).Render(appName))

	badgeStart := appW + 1
	badgeW := lipgloss.Width(m.st.accentBlock.Render(m.theme.Display))
	badgeEnd := badgeStart + badgeW

	tabStart := badgeEnd + 3
	tabAllText := m.st.tabInactive.Render("ALL")
	if m.tab == TabAll {
		tabAllText = m.st.tabActive.Render("◆ ALL ◆")
	}
	tabAllW := lipgloss.Width(tabAllText)
	tabAllEnd := tabStart + tabAllW

	tabFavsStart := tabAllEnd + 2
	tabFavsText := m.st.tabInactive.Render("FAVORITES")
	if m.tab == TabFavorites {
		tabFavsText = m.st.tabActive.Render("◆ FAVORITES ◆")
	}
	tabFavsW := lipgloss.Width(tabFavsText)
	tabFavsEnd := tabFavsStart + tabFavsW

	switch {
	case x >= badgeStart && x < badgeEnd:
		m.focus = FocusNetworkPicker
		m.netCursor = networkIdxForSlug(m.currentNetwork)
		return m, nil

	case x >= tabStart && x < tabAllEnd:
		if m.tab != TabAll {
			m.tab = TabAll
			m.selIdx = 0
			return m, tea.Batch(m.kickoffVisibleThumbs()...)
		}
		return m, nil

	case x >= tabFavsStart && x < tabFavsEnd:
		if m.tab != TabFavorites {
			m.tab = TabFavorites
			m.selIdx = 0
			return m, tea.Batch(m.kickoffVisibleThumbs()...)
		}
		return m, nil
	}

	return m, nil
}

// cardIndexFromY maps a terminal Y coordinate to a visible-channel index.
// Returns false when the click is outside the card area or beyond the
// last visible card.
func (m Model) cardIndexFromY(y int, vis []audioaddict.Channel) (int, bool) {
	if y < channelCardsStartY {
		return 0, false
	}

	vpStart := m.computeViewportStart(vis)

	relRow := y - channelCardsStartY
	cardOffset := relRow / cardHeight
	idx := vpStart + cardOffset

	if idx < 0 || idx >= len(vis) {
		return 0, false
	}
	return idx, true
}

// computeViewportStart replicates the centered-on-selection viewport math
// from renderChannels so the mouse handler agrees with what's on screen.
func (m Model) computeViewportStart(vis []audioaddict.Channel) int {
	innerH := m.height - 5
	bodyH := innerH - 2
	if bodyH < cardHeight {
		bodyH = cardHeight
	}
	cardsPerView := bodyH / cardHeight
	if cardsPerView < 1 {
		cardsPerView = 1
	}

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
	return start
}
