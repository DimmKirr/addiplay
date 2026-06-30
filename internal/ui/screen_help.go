package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// helpEntry pairs a key binding with the section it belongs to. Sections
// keep the overlay organized — listeners scan vertically by category
// (navigation / playback / voting / app) rather than chasing keys.
type helpEntry struct {
	section string
	binding key.Binding
}

// helpEntries is the SINGLE source of truth for what the `?` overlay
// shows. Adding a new key binding without an entry here means the
// keymap promise on the bottom hint ("[?] more") is again broken. The
// linter for that (TestHelp_questionMarkOpensOverlay) covers the most
// common bindings; the rest must be manually added below.
var helpEntries = []helpEntry{
	{"navigation", keys.Up},
	{"navigation", keys.Down},
	{"navigation", keys.SwitchTab},
	{"navigation", keys.Network},
	{"navigation", keys.Search},
	{"playback", keys.Play},
	{"playback", keys.PauseResume},
	{"playback", keys.VolumeUp},
	{"playback", keys.VolumeDown},
	{"voting", keys.Like},
	{"voting", keys.Dislike},
	{"voting", keys.Favorite},
	{"app", keys.Help},
	{"app", keys.Logout},
	{"app", keys.Quit},
}

// updateHelp owns the FocusHelp lifecycle: Esc / `?` / `q` close,
// everything else is swallowed (no scroll yet; the overlay fits).
func (m Model) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc,
		key.Matches(msg, keys.Help),
		key.Matches(msg, keys.Quit) && msg.Type == tea.KeyRunes:
		m.focus = m.prevFocus
		return m, nil
	}
	return m, nil
}

// viewHelp renders the keymap overlay — title + sections + close hint.
func (m Model) viewHelp() string {
	sections := []string{"navigation", "playback", "voting", "app"}
	maxKeyW := 0
	for _, e := range helpEntries {
		k, _ := e.binding.Help().Key, e.binding.Help().Desc
		if w := len(k); w > maxKeyW {
			maxKeyW = w
		}
	}
	var rows []string
	rows = append(rows, m.st.header.Bold(true).Render("Keybindings"))
	rows = append(rows, m.st.muted.Render(strings.Repeat("─", 48)))
	for _, sec := range sections {
		rows = append(rows, "")
		rows = append(rows, m.st.muted.Render(strings.ToUpper(sec)))
		for _, e := range helpEntries {
			if e.section != sec {
				continue
			}
			h := e.binding.Help()
			padding := strings.Repeat(" ", maxKeyW-len(h.Key)+2)
			row := m.st.accentBlock.Padding(0, 1).Render(h.Key) + padding + h.Desc
			rows = append(rows, row)
		}
	}
	rows = append(rows, "")
	rows = append(rows, m.st.keyHint.Render("[esc] close   [?] toggle"))
	return m.renderCenteredPopover(strings.Join(rows, "\n"), 3, 1)
}
