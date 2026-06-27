package fanart

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// TestUnicodePlaceholder_isOneCellWide checks the foundation of Kitty's
// Unicode-placeholder layout integration: each U+10EEEE placeholder MUST
// be measured as exactly one terminal cell by go-runewidth (what lipgloss
// uses for width math). If runewidth treats it as 0 or 2, lipgloss will
// mis-align card layouts and we'd have to fall back to ASCII or build a
// post-render injection scheme.
func TestUnicodePlaceholder_isOneCellWide(t *testing.T) {
	const ph = "\U0010EEEE"
	if w := runewidth.RuneWidth('\U0010EEEE'); w != 1 {
		t.Fatalf("runewidth.RuneWidth(U+10EEEE) = %d; need 1 for Kitty placeholder + lipgloss to coexist", w)
	}
	if w := runewidth.StringWidth(strings.Repeat(ph, 16)); w != 16 {
		t.Errorf("StringWidth of 16 placeholders = %d; want 16", w)
	}
	// lipgloss.Width is the authoritative measure used by JoinHorizontal.
	if w := lipgloss.Width(strings.Repeat(ph, 16)); w != 16 {
		t.Errorf("lipgloss.Width of 16 placeholders = %d; want 16", w)
	}
}
