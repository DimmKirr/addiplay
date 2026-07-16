package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
)

// --- DIMM-422: Mouse click to select channel, scroll wheel, header clicks ---

// channelPaneCardY returns the terminal Y coordinate of the top of the
// n-th visible card (0-based) in the channel pane. This mirrors the
// layout math in renderChannels + viewHome:
//
//	row 0: header (renderHeader)
//	row 1: pane border top
//	row 2: channel count / search input
//	row 3: separator ("───")
//	row 4+: cards, each cardHeight rows tall
func channelPaneCardY(cardIndex int) int {
	return 4 + cardIndex*cardHeight
}

// headerClickX returns an X coordinate guaranteed to be inside the
// given header element. Recomputes the layout widths the same way
// renderHeader does so it tracks any future changes.
func headerClickX(m Model, target string) int {
	appName := "addiplay"
	if m.width < 100 {
		appName = "add"
	}
	appW := lipgloss.Width(m.st.header.Bold(true).Render(appName))
	badgeW := lipgloss.Width(m.st.accentBlock.Render(m.theme.Display))

	// Layout: app(appW) + " "(1) + badge(badgeW) + "   "(3) + tabAll + "  "(2) + tabFavs
	tabAllText := m.st.tabInactive.Render("ALL")
	if m.tab == TabAll {
		tabAllText = m.st.tabActive.Render("◆ ALL ◆")
	}
	tabAllW := lipgloss.Width(tabAllText)

	tabStart := appW + 1 + badgeW + 3

	switch target {
	case "badge":
		return appW + 1 + badgeW/2
	case "tabAll":
		return tabStart + tabAllW/2
	case "tabFavs":
		return tabStart + tabAllW + 2 + 2
	}
	return 0
}

func testChannels(n int) []audioaddict.Channel {
	chs := make([]audioaddict.Channel, n)
	keys := []string{
		"vocaltrance", "chillout", "classictrance", "house",
		"deephouse", "techno", "minimal", "progressive",
		"drumandbass", "ambient",
	}
	for i := range chs {
		k := keys[i%len(keys)]
		chs[i] = audioaddict.Channel{
			ID:   int64(i + 1),
			Key:  k,
			Name: k,
		}
	}
	return chs
}

// TestMouseClick_selectsCard verifies that a left-click on a card
// that is NOT currently selected changes selIdx to that card.
func TestMouseClick_selectsCard(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(5)
	m.selIdx = 0

	y := channelPaneCardY(2)
	m2, _ := m.Update(tea.MouseMsg{
		X:      10,
		Y:      y,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.selIdx != 2 {
		t.Errorf("selIdx = %d; want 2 after clicking card 2", mm.selIdx)
	}
}

// TestMouseClick_selectedCardPlays verifies that clicking an already-
// selected card triggers playback (same as pressing Enter).
func TestMouseClick_selectedCardPlays(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(5)
	m.selIdx = 1

	y := channelPaneCardY(1)
	m2, cmd := m.Update(tea.MouseMsg{
		X:      10,
		Y:      y,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.selIdx != 1 {
		t.Errorf("selIdx = %d; want 1 (unchanged)", mm.selIdx)
	}
	if !mm.resolving && cmd == nil {
		t.Error("expected playback to start (resolving=true or cmd returned) when clicking already-selected card")
	}
}

// TestMouseWheel_down moves selection down by one.
func TestMouseWheel_down(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(5)
	m.selIdx = 0

	m2, _ := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelDown,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.selIdx != 1 {
		t.Errorf("selIdx = %d; want 1 after wheel down", mm.selIdx)
	}
}

// TestMouseWheel_up moves selection up by one.
func TestMouseWheel_up(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(5)
	m.selIdx = 3

	m2, _ := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelUp,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.selIdx != 2 {
		t.Errorf("selIdx = %d; want 2 after wheel up", mm.selIdx)
	}
}

// TestMouseWheel_downClamps verifies wheel-down at the last channel
// does not overflow.
func TestMouseWheel_downClamps(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(3)
	m.selIdx = 2

	m2, _ := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelDown,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.selIdx != 2 {
		t.Errorf("selIdx = %d; want 2 (clamped at last)", mm.selIdx)
	}
}

// TestMouseWheel_upClamps verifies wheel-up at the first channel
// does not underflow.
func TestMouseWheel_upClamps(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(3)
	m.selIdx = 0

	m2, _ := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelUp,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.selIdx != 0 {
		t.Errorf("selIdx = %d; want 0 (clamped at first)", mm.selIdx)
	}
}

// TestMouseClick_outsideCards is a no-op (selIdx unchanged).
func TestMouseClick_outsideCards(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(3)
	m.selIdx = 1

	m2, _ := m.Update(tea.MouseMsg{
		X:      10,
		Y:      0, // header row
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.selIdx != 1 {
		t.Errorf("selIdx = %d; want 1 (unchanged on header click)", mm.selIdx)
	}
}

// TestMouseClick_beyondLastCard is a no-op when clicking below the
// last visible card.
func TestMouseClick_beyondLastCard(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(2)
	m.selIdx = 0

	y := channelPaneCardY(5) // well past the 2 channels
	m2, _ := m.Update(tea.MouseMsg{
		X:      10,
		Y:      y,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.selIdx != 0 {
		t.Errorf("selIdx = %d; want 0 (unchanged on click past last card)", mm.selIdx)
	}
}

// TestMouseClick_ignoredDuringOverlay verifies that mouse clicks are
// ignored when a non-home overlay (login, network picker, etc.) is active.
func TestMouseClick_ignoredDuringOverlay(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(3)
	m.selIdx = 0
	m.focus = FocusLogin

	y := channelPaneCardY(2)
	m2, _ := m.Update(tea.MouseMsg{
		X:      10,
		Y:      y,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.selIdx != 0 {
		t.Errorf("selIdx = %d; want 0 (unchanged during login overlay)", mm.selIdx)
	}
}

// --- Header click tests ---

// TestMouseClick_tabAll switches to the ALL tab when clicked.
func TestMouseClick_tabAll(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(5)
	m.tab = TabFavorites
	m.selIdx = 2

	x := headerClickX(m, "tabAll")
	m2, _ := m.Update(tea.MouseMsg{
		X:      x,
		Y:      0,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.tab != TabAll {
		t.Errorf("tab = %d; want TabAll (%d)", mm.tab, TabAll)
	}
	if mm.selIdx != 0 {
		t.Errorf("selIdx = %d; want 0 (reset on tab switch)", mm.selIdx)
	}
}

// TestMouseClick_tabFavorites switches to FAVORITES tab when clicked.
func TestMouseClick_tabFavorites(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(5)
	m.tab = TabAll
	m.selIdx = 3

	x := headerClickX(m, "tabFavs")
	m2, _ := m.Update(tea.MouseMsg{
		X:      x,
		Y:      0,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.tab != TabFavorites {
		t.Errorf("tab = %d; want TabFavorites (%d)", mm.tab, TabFavorites)
	}
	if mm.selIdx != 0 {
		t.Errorf("selIdx = %d; want 0 (reset on tab switch)", mm.selIdx)
	}
}

// TestMouseClick_networkBadge opens the network picker overlay.
func TestMouseClick_networkBadge(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(3)

	x := headerClickX(m, "badge")
	m2, _ := m.Update(tea.MouseMsg{
		X:      x,
		Y:      0,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.focus != FocusNetworkPicker {
		t.Errorf("focus = %d; want FocusNetworkPicker (%d)", mm.focus, FocusNetworkPicker)
	}
}

// TestMouseClick_tabNoopWhenAlreadyActive verifies clicking the active
// tab doesn't reset selIdx unnecessarily.
func TestMouseClick_tabNoopWhenAlreadyActive(t *testing.T) {
	m := newTestModel(t)
	m.channels = testChannels(5)
	m.tab = TabAll
	m.selIdx = 3

	x := headerClickX(m, "tabAll")
	m2, _ := m.Update(tea.MouseMsg{
		X:      x,
		Y:      0,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	mm := m2.(Model)

	if mm.tab != TabAll {
		t.Errorf("tab should remain TabAll")
	}
	if mm.selIdx != 3 {
		t.Errorf("selIdx = %d; want 3 (unchanged when clicking already-active tab)", mm.selIdx)
	}
}
