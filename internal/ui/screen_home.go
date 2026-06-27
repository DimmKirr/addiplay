package ui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/fanart"
	"github.com/dimmkirr/addiplay/internal/player"
)

// updateHome handles keys for the main screen (channel list focused, no
// overlay active). All transport, navigation, favourites, tab-switching,
// volume and overlay-open keys live here.
func (m Model) updateHome(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Quit):
		dlog("quit key received (type=%s)", msg.Type)
		// Cancel m.ctx FIRST so every in-flight fanart/thumb/track HTTP
		// fetch returns context.Canceled immediately. Without this, the
		// process appears to "freeze on exit" for up to 10s while goroutines
		// drain their HTTP timeouts before main can exit.
		if m.cancel != nil {
			m.cancel()
			dlog("quit: m.cancel() returned")
		}
		if m.player != nil {
			err := m.player.Close()
			dlog("quit: m.player.Close() returned (err=%v)", err)
		}
		dlog("quit: returning tea.Quit")
		return m, tea.Quit

	case key.Matches(msg, keys.Down):
		if m.selIdx < len(m.visibleChannels())-1 {
			m.selIdx++
		}
		return m, tea.Batch(m.kickoffVisibleThumbs()...)
	case key.Matches(msg, keys.Up):
		if m.selIdx > 0 {
			m.selIdx--
		}
		return m, tea.Batch(m.kickoffVisibleThumbs()...)

	case key.Matches(msg, keys.Play):
		// Drop Enter while a prior playSelectedCmd is in flight so holding
		// Enter doesn't fire N parallel resolver HTTPs.
		if m.resolving {
			break
		}
		if ch, ok := m.selectedChannel(); ok && m.player != nil {
			m.toast = ""
			m.resolving = true
			return m, playSelectedCmd(m.ctx, m.client, m.player, m.currentNetwork, ch, m.creds.ListenKey)
		}

	case key.Matches(msg, keys.PauseResume):
		if m.player != nil {
			switch m.playerSt {
			case player.StatePlaying:
				if err := m.player.Pause(); err != nil {
					m.toast = "pause: " + err.Error()
				}
			case player.StatePaused:
				if err := m.player.Resume(); err != nil {
					m.toast = "resume: " + err.Error()
				}
			}
		}

	case key.Matches(msg, keys.VolumeUp):
		m.cfg.Volume = clamp(m.cfg.Volume+5, 0, 100)
		if m.player != nil {
			if err := m.player.SetVolume(m.cfg.Volume); err != nil {
				m.toast = "volume: " + err.Error()
			}
		}
		_ = m.cfg.Save()

	case key.Matches(msg, keys.VolumeDown):
		m.cfg.Volume = clamp(m.cfg.Volume-5, 0, 100)
		if m.player != nil {
			if err := m.player.SetVolume(m.cfg.Volume); err != nil {
				m.toast = "volume: " + err.Error()
			}
		}
		_ = m.cfg.Save()

	case key.Matches(msg, keys.Favorite):
		if ch, ok := m.selectedChannel(); ok {
			if m.cfg.IsFavorite(m.currentNetwork, ch.Key) {
				m.cfg.RemoveFavorite(m.currentNetwork, ch.Key)
			} else {
				m.cfg.AddFavorite(m.currentNetwork, ch.Key)
			}
			_ = m.cfg.Save()
		}

	case key.Matches(msg, keys.SwitchTab):
		if m.tab == TabAll {
			m.tab = TabFavorites
		} else {
			m.tab = TabAll
		}
		m.selIdx = 0

	case key.Matches(msg, keys.Search):
		m.focus = FocusSearch
		m.searchInput.Focus()
		m.searchInput.SetValue("")

	case key.Matches(msg, keys.Network):
		m.focus = FocusNetworkPicker
		m.netCursor = networkIdxForSlug(m.currentNetwork)

	case key.Matches(msg, keys.Logout):
		// In-TUI logout: clear creds, stop playback, drop back to the
		// login overlay so the user can sign in as someone else (or
		// reset a wedged session) without leaving the binary. Mirrors
		// the headless `addiplay --logout` action (cmd/auth.go).
		dlog("logout key received")
		if err := creds.Clear(); err != nil {
			m.toast = "logout: " + err.Error()
			break
		}
		if m.player != nil {
			_ = m.player.Stop()
		}
		// Wipe in-memory session state so the model looks freshly
		// constructed; the next channels-load attempt will 401 and the
		// login overlay handler is identical to the auto-pop path.
		m.creds = creds.Creds{}
		m.currentChannel = ""
		m.currentTrack = audioaddict.Track{}
		m.fanartEscape = ""
		m.fanartSourceURL = ""
		m.toast = "signed out"
		m = m.initLoginInputs(true)
	}
	return m, nil
}

// viewHome renders the full home frame: header, body (channels list +
// optional Now Playing column), and the status bar/hints at the bottom.
func (m Model) viewHome() string {
	header := renderHeader(m)
	footer := renderStatus(m)
	body := m.renderBody()

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		body,
		footer,
	)
}

// renderBody decides the body layout: channel list alone when the screen
// is narrow or nothing is playing, channel list + Now Playing column
// otherwise. The art-column width matches the active fanart mode so its
// content doesn't lipgloss-wrap at a smaller pane width.
func (m Model) renderBody() string {
	if m.height < 8 {
		return ""
	}
	// One row for header, three for footer (status + hints + optional toast).
	innerH := m.height - 5

	showArt := m.currentChannel != "" && m.width >= minWidthForArt
	if !showArt {
		return renderChannels(m, m.width-2, innerH-2)
	}

	_, _, colWidth := fanartDimensions(fanart.DetectMode())
	listW := m.width - colWidth - 2
	left := renderChannels(m, listW, innerH-2)
	right := m.renderNowPlaying(colWidth, innerH)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}
