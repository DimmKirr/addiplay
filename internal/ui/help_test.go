package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHelp_questionMarkOpensOverlay enforces DIMM-392: the `?` key
// promised in the header (`internal/ui/header.go`) must actually
// open a help overlay. The view should clearly title itself and
// list every keybinding the home screen advertises.
func TestHelp_questionMarkOpensOverlay(t *testing.T) {
	m := newTestModel(t)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = sized.(Model)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	got := updated.(Model)

	if got.focus != FocusHelp {
		t.Fatalf("after `?`, focus = %d; want FocusHelp", got.focus)
	}
	view := got.View()
	if !strings.Contains(view, "Keybindings") {
		t.Errorf("help overlay missing 'Keybindings' title; got:\n%s", view)
	}
	// Spot-check that key actions surface here even though DIMM-394
	// trimmed them out of the always-visible footer.
	for _, must := range []string{"like", "dislike", "favorite", "network", "search", "quit", "logout"} {
		if !strings.Contains(view, must) {
			t.Errorf("help overlay missing description for %q; got:\n%s", must, view)
		}
	}
}

// TestHelp_escClosesOverlay — pressing Esc returns to the previous focus.
func TestHelp_escClosesOverlay(t *testing.T) {
	m := newTestModel(t)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = sized.(Model)

	// Open
	opened, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	mm := opened.(Model)
	if mm.focus != FocusHelp {
		t.Fatalf("setup failed: focus = %d", mm.focus)
	}
	// Close
	closed, _ := mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	cc := closed.(Model)
	if cc.focus == FocusHelp {
		t.Errorf("after esc, focus is still FocusHelp; expected to return to previous focus")
	}
}
