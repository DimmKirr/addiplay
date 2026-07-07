package ui

import (
	"strings"
	"testing"
)

// TestHintsText_focusChannels_keepsItShort enforces DIMM-394: the bottom
// hint row should show no more than 5 items (4 actions + `[?] more`).
// The original 9-item wall-of-text was readable on extra-wide terminals
// only and wrapped on narrow ones. Convention adopted from lazygit /
// helix / lazydocker: short context hints + a single discoverability
// pointer (`[?]`).
func TestHintsText_focusChannels_keepsItShort(t *testing.T) {
	m := newTestModel(t)
	m.focus = FocusChannels

	got := hintsText(m)
	items := strings.Count(got, "[")
	if items > 5 {
		t.Errorf("FocusChannels hint row has %d items; want at most 5 (4 actions + `?` pointer)\ngot: %s", items, got)
	}
	if !strings.Contains(got, "[?]") {
		t.Errorf("FocusChannels hint row missing `[?]` pointer to full help; got: %s", got)
	}
}

// TestHintsText_focusSearch_isContextual ensures focus-specific hints
// are preserved (search shows search-specific hints, not the home set).
func TestHintsText_focusSearch_isContextual(t *testing.T) {
	m := newTestModel(t)
	m.focus = FocusSearch

	got := hintsText(m)
	if !strings.Contains(got, "esc") {
		t.Errorf("FocusSearch hint row should mention esc; got: %s", got)
	}
	if strings.Contains(got, "[l]") || strings.Contains(got, "[s]") {
		t.Errorf("FocusSearch hint row leaked home-screen bindings (like/dislike); got: %s", got)
	}
}

// TestHintsText_focusNetworkPicker_isContextual same check for the
// network picker focus.
func TestHintsText_focusNetworkPicker_isContextual(t *testing.T) {
	m := newTestModel(t)
	m.focus = FocusNetworkPicker

	got := hintsText(m)
	if !strings.Contains(got, "esc") {
		t.Errorf("FocusNetworkPicker hint row should mention esc; got: %s", got)
	}
}
