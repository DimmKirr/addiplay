package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Version is set by the binary entrypoint (cmd/tui.go) before the TUI starts.
var Version = "dev"

// DebugMode is set to true when --debug is passed. Controls whether the
// About screen shows the full terminal color palette.
var DebugMode bool

// paletteColsPerRow is used for cursor navigation in the color grid.
const paletteColsPerRow = 16

func (m Model) handleAboutMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelDown:
		m.aboutScroll += 3
	case tea.MouseButtonWheelUp:
		m.aboutScroll -= 3
		if m.aboutScroll < 0 {
			m.aboutScroll = 0
		}
	}
	return m, nil
}

func (m Model) updateAbout(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc,
		key.Matches(msg, keys.About),
		key.Matches(msg, keys.Quit) && msg.Type == tea.KeyRunes:
		m.focus = m.prevFocus
		return m, nil

	case msg.Type == tea.KeyRight || msg.String() == "l":
		if DebugMode && m.aboutCursor < 255 {
			m.aboutCursor++
		}
	case msg.Type == tea.KeyLeft || msg.String() == "h":
		if DebugMode && m.aboutCursor > 0 {
			m.aboutCursor--
		}
	case key.Matches(msg, keys.Down) || msg.String() == "j":
		if DebugMode {
			m.aboutCursor += paletteColsPerRow
			if m.aboutCursor > 255 {
				m.aboutCursor = 255
			}
		} else {
			m.aboutScroll += 3
		}
	case key.Matches(msg, keys.Up) || msg.String() == "k":
		if DebugMode {
			m.aboutCursor -= paletteColsPerRow
			if m.aboutCursor < 0 {
				m.aboutCursor = 0
			}
		} else {
			m.aboutScroll -= 3
			if m.aboutScroll < 0 {
				m.aboutScroll = 0
			}
		}
	case msg.Type == tea.KeyPgDown:
		m.aboutScroll += 10
	case msg.Type == tea.KeyPgUp:
		m.aboutScroll -= 10
		if m.aboutScroll < 0 {
			m.aboutScroll = 0
		}

	case msg.Type == tea.KeyEnter || msg.String() == "y":
		if DebugMode {
			code := fmt.Sprintf("%d", m.aboutCursor)
			m.toast = fmt.Sprintf("copied: ANSI %s", code)
			m.toastIsWarn = false
			return m, copyToClipboardCmd(code)
		}
	}
	return m, nil
}

// copyToClipboardCmd writes text to the terminal clipboard via OSC 52.
func copyToClipboardCmd(text string) tea.Cmd {
	return func() tea.Msg {
		b64 := base64.StdEncoding.EncodeToString([]byte(text))
		seq := fmt.Sprintf("\x1b]52;c;%s\x07", b64)
		_, _ = os.Stdout.Write([]byte(seq))
		return nil
	}
}

func (m Model) viewAbout() string {
	w := m.width
	if w < 20 {
		w = 80
	}
	contentW := w - 4

	var lines []string

	// Header
	title := m.st.header.Bold(true).Render("About addiplay")
	lines = append(lines, title)
	lines = append(lines, m.st.muted.Render(strings.Repeat("─", min(contentW, 60))))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  Version:  %s", Version))
	lines = append(lines, fmt.Sprintf("  Network:  %s", m.theme.Display))
	lines = append(lines, "  Source:   github.com/DimmKirr/addiplay")
	lines = append(lines, "")
	lines = append(lines, m.st.muted.Render("  A terminal music player for AudioAddict networks"))
	lines = append(lines, m.st.muted.Render("  (DI.fm, RadioTunes, RockRadio, JazzRadio, ClassicalRadio, ZenRadio)"))
	lines = append(lines, "")

	if DebugMode {
		lines = append(lines, m.st.header.Bold(true).Render("Terminal Color Palette (256 colors)"))
		lines = append(lines, m.st.muted.Render(strings.Repeat("─", min(contentW, 60))))
		lines = append(lines, "")

		// Selected color info bar
		selColor := lipgloss.Color(fmt.Sprintf("%d", m.aboutCursor))
		selBg := lipgloss.NewStyle().Background(selColor).Render("      ")
		selFg := lipgloss.NewStyle().Foreground(selColor).Render("██████")
		lines = append(lines, fmt.Sprintf("  Selected: ANSI %3d  %s  %s   [enter/y to copy]",
			m.aboutCursor, selBg, selFg))
		lines = append(lines, "")

		// Render the full 256-color grid (16 columns)
		lines = append(lines, m.st.accentBlock.Render(" 256-COLOR GRID "))
		lines = append(lines, "")

		for row := 0; row < 256; row += paletteColsPerRow {
			var rowBuf strings.Builder
			rowBuf.WriteString("  ")
			end := row + paletteColsPerRow
			if end > 256 {
				end = 256
			}
			for i := row; i < end; i++ {
				c := lipgloss.Color(fmt.Sprintf("%d", i))
				label := fmt.Sprintf("%3d", i)
				if i == m.aboutCursor {
					// Highlighted: invert colors for the cursor
					s := lipgloss.NewStyle().
						Background(lipgloss.Color("15")).
						Foreground(c).
						Bold(true).
						Render("[" + label + "]")
					rowBuf.WriteString(s)
				} else {
					s := lipgloss.NewStyle().Background(c).Foreground(lipgloss.Color("15")).Render(" " + label + " ")
					rowBuf.WriteString(s)
				}
			}
			lines = append(lines, rowBuf.String())
		}
		lines = append(lines, "")
		lines = append(lines, "")

		// Block characters / textures in selected color
		lines = append(lines, m.st.accentBlock.Render(" TEXTURES (in selected color) "))
		lines = append(lines, "")

		blocks := []struct {
			label string
			char  string
		}{
			{"Full block", "████████████"},
			{"Light shade", "░░░░░░░░░░░░"},
			{"Medium shade", "▒▒▒▒▒▒▒▒▒▒▒▒"},
			{"Dark shade", "▓▓▓▓▓▓▓▓▓▓▓▓"},
			{"Upper half", "▀▀▀▀▀▀▀▀▀▀▀▀"},
			{"Lower half", "▄▄▄▄▄▄▄▄▄▄▄▄"},
			{"Left half", "▌▌▌▌▌▌▌▌▌▌▌▌"},
			{"Braille full", "⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿"},
			{"Braille dots", "⡀⠄⠂⠁⡀⠄⠂⠁⡀⠄⠂⠁"},
			{"Horizontal", "────────────"},
			{"Double horiz", "════════════"},
			{"Dashed", "┄┄┄┄┄┄┄┄┄┄┄┄"},
			{"Dotted", "┈┈┈┈┈┈┈┈┈┈┈┈"},
		}

		for _, b := range blocks {
			fgStyle := lipgloss.NewStyle().Foreground(selColor).Render(b.char)
			bgStyle := lipgloss.NewStyle().Background(selColor).Foreground(lipgloss.Color("15")).Render(b.char)
			lines = append(lines, fmt.Sprintf("  %-16s fg: %s  bg: %s", b.label, fgStyle, bgStyle))
		}
		lines = append(lines, "")
		lines = append(lines, "")

		// Text styles in selected color
		lines = append(lines, m.st.accentBlock.Render(" TEXT STYLES (in selected color) "))
		lines = append(lines, "")
		sample := "The quick brown fox"
		styleExamples := []struct {
			name  string
			style lipgloss.Style
		}{
			{"Normal", lipgloss.NewStyle().Foreground(selColor)},
			{"Bold", lipgloss.NewStyle().Foreground(selColor).Bold(true)},
			{"Italic", lipgloss.NewStyle().Foreground(selColor).Italic(true)},
			{"Underline", lipgloss.NewStyle().Foreground(selColor).Underline(true)},
			{"Strikethrough", lipgloss.NewStyle().Foreground(selColor).Strikethrough(true)},
			{"On white bg", lipgloss.NewStyle().Foreground(selColor).Background(lipgloss.Color("15"))},
			{"On black bg", lipgloss.NewStyle().Foreground(selColor).Background(lipgloss.Color("0"))},
			{"Reverse", lipgloss.NewStyle().Foreground(selColor).Reverse(true)},
		}
		for _, ex := range styleExamples {
			lines = append(lines, fmt.Sprintf("  %-16s %s", ex.name+":", ex.style.Render(sample)))
		}
		lines = append(lines, "")
		lines = append(lines, "")

		// Current theme
		lines = append(lines, m.st.accentBlock.Render(" CURRENT THEME: "+strings.ToUpper(m.theme.Display)+" "))
		lines = append(lines, "")
		themeSwatches := []struct {
			name  string
			color lipgloss.Color
		}{
			{"Accent", m.theme.Accent},
			{"Secondary", m.theme.Secondary},
			{"Pop", m.theme.Pop},
			{"FG", m.theme.FG},
			{"FGMuted", m.theme.FGMuted},
			{"BG", m.theme.BG},
			{"BGAlt", m.theme.BGAlt},
			{"Success", m.theme.Success},
			{"Warn", m.theme.Warn},
			{"Error", m.theme.Error},
		}
		for _, ts := range themeSwatches {
			bgSwatch := lipgloss.NewStyle().Background(ts.color).Render("      ")
			fgSwatch := lipgloss.NewStyle().Foreground(ts.color).Render("██████")
			lines = append(lines, fmt.Sprintf("  %-12s %s  %s  %s", ts.name, bgSwatch, fgSwatch, string(ts.color)))
		}
		lines = append(lines, "")
		lines = append(lines, "")
	}

	// Footer
	if DebugMode {
		lines = append(lines, m.st.keyHint.Render("  [←/→/↑/↓] navigate   [enter/y] copy ANSI code   [pgup/pgdn] scroll   [esc/A/q] close"))
	} else {
		lines = append(lines, m.st.keyHint.Render("  [esc/A/q] close"))
	}

	// Apply scroll
	totalLines := len(lines)
	viewH := m.height - 2
	if viewH < 1 {
		viewH = 1
	}

	maxScroll := totalLines - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.aboutScroll > maxScroll {
		m.aboutScroll = maxScroll
	}

	start := m.aboutScroll
	end := start + viewH
	if end > totalLines {
		end = totalLines
	}

	visible := lines[start:end]
	content := strings.Join(visible, "\n")

	// Prepend OSC 52 clipboard sequence if a copy was just triggered
	return m.st.app.Width(w).Height(m.height).Render(content)
}

