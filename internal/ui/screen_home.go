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
	// Any key (other than the `L` re-press handled in keys.Logout below)
	// while a logout confirmation is pending cancels the pending state.
	// Keeps the safety net real: a stray key shouldn't leave the toast
	// hanging in "press L again" limbo.
	if m.pendingLogout {
		if msg.Type == tea.KeyEsc {
			m.pendingLogout = false
			m.toast = ""
			return m, nil
		}
		// Non-`L` keys also clear the pending state so the toast goes
		// away as the user continues. The `L` case below handles its
		// own state transition.
		if !key.Matches(msg, keys.Logout) {
			m.pendingLogout = false
			m.toast = ""
			// fall through — the actual key still gets routed
		}
	}
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
			return m, playSelectedCmd(m.ctx, m.client, m.player, m.currentNetwork, ch)
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

	case key.Matches(msg, keys.Like):
		dlog("Like key matched (`l`): currentTrack.ID=%d voteInFlight=%t currentChannel=%q playingNetwork=%q session_key_len=%d",
			m.currentTrack.ID, m.voteInFlight, m.currentChannel, m.playingNetwork, len(m.creds.SessionKey))
		if dir, ok := m.prepareVote(voteUp); ok {
			channelID := channelIDFromKey(m.channels, m.currentChannel)
			m.voteInFlight = true
			dlog("Like key: dispatching voteCmd (network=%s track=%d channel=%d dir=%d)",
				m.playingNetwork, m.currentTrack.ID, channelID, dir)
			return m, voteCmd(m.ctx, m.client, m.playingNetwork, m.currentTrack.ID, channelID, dir)
		}
		dlog("Like key: prepareVote returned ok=false — no Cmd dispatched")

	case key.Matches(msg, keys.Dislike):
		dlog("Dislike key matched (`s`): currentTrack.ID=%d voteInFlight=%t currentChannel=%q playingNetwork=%q session_key_len=%d",
			m.currentTrack.ID, m.voteInFlight, m.currentChannel, m.playingNetwork, len(m.creds.SessionKey))
		if dir, ok := m.prepareVote(voteDown); ok {
			channelID := channelIDFromKey(m.channels, m.currentChannel)
			m.voteInFlight = true
			dlog("Dislike key: dispatching voteCmd (network=%s track=%d channel=%d dir=%d)",
				m.playingNetwork, m.currentTrack.ID, channelID, dir)
			return m, voteCmd(m.ctx, m.client, m.playingNetwork, m.currentTrack.ID, channelID, dir)
		}
		dlog("Dislike key: prepareVote returned ok=false — no Cmd dispatched")

	case key.Matches(msg, keys.Help):
		// DIMM-392: open the keymap overlay. Esc / `?` / `q` close it.
		// `prevFocus` lets the overlay restore to whichever screen the
		// user came from (currently always FocusChannels from this
		// handler, but kept generic so other focus handlers can route
		// through the same overlay later).
		dlog("Help key matched (`?`): opening overlay (prevFocus=%d)", m.focus)
		m.prevFocus = m.focus
		m.focus = FocusHelp
		return m, nil

	case key.Matches(msg, keys.Logout):
		// DIMM-393: confirm-then-act for the destructive logout. First
		// `L` arms `pendingLogout`; the second within ~3s actually
		// wipes the session (mirrors `addiplay --logout` / cmd/auth.go).
		// Anything else (esc / unrelated key) clears the pending flag.
		if !m.pendingLogout {
			dlog("Logout key (`L`): arming confirmation")
			m.pendingLogout = true
			m.toast = "press L again to confirm logout · esc cancels"
			return m, logoutConfirmTimeoutCmd()
		}
		dlog("Logout key (`L`) again: confirmed — clearing session")
		m.pendingLogout = false
		if err := m.client.Logout(); err != nil {
			m.toast = "logout: " + err.Error()
			break
		}
		if m.player != nil {
			_ = m.player.Stop()
		}
		// Wipe in-memory session state so the model looks freshly
		// constructed; the next channels-load attempt will 401 and the
		// login overlay handler is identical to the auto-pop path.
		m.creds = creds.Session{}
		m.currentChannel = ""
		m.currentTrack = audioaddict.Track{}
		m.fanartEscape = ""
		m.fanartSourceURL = ""
		m.toast = "signed out"
		m = m.initLoginInputs(true)
	}
	return m, nil
}

// prepareVote validates the preconditions for a vote action and returns
// the actual direction to send. `desired` is what the user expressed by
// pressing the key (voteUp for `l`, voteDown for `s`); the returned
// direction is voteClear when the user pressed the key on a track that
// already carries that vote (toggle off). ok==false means the press
// should be ignored entirely (no track, missing channel, mid-flight,
// stale session key).
func (m *Model) prepareVote(desired voteDirection) (voteDirection, bool) {
	chID := channelIDFromKey(m.channels, m.currentChannel)
	dlog("prepareVote desired=%d currentTrack.ID=%d voteInFlight=%t currentChannel=%q channelID=%d session_key_len=%d liked=%t disliked=%t",
		desired, m.currentTrack.ID, m.voteInFlight, m.currentChannel, chID, len(m.creds.SessionKey),
		m.likedTracks[m.currentTrack.ID], m.dislikedTracks[m.currentTrack.ID])
	if m.currentTrack.ID == 0 || m.voteInFlight {
		dlog("prepareVote DROP: no track or vote in flight")
		return 0, false
	}
	if chID == 0 {
		dlog("prepareVote DROP: channelID==0 (currentChannel=%q channels_loaded=%d)", m.currentChannel, len(m.channels))
		return 0, false
	}
	if m.creds.SessionKey == "" {
		// Old credfile (pre-DIMM-381) — surface the same "sign in
		// again" path the unauthorized handlers use so the user
		// gets a fresh session_key.
		dlog("prepareVote DROP: empty SessionKey — popping login overlay")
		m.toast = "session expired — sign in again"
		*m = m.initLoginInputs(false)
		return 0, false
	}
	id := m.currentTrack.ID
	switch desired {
	case voteUp:
		if m.likedTracks[id] {
			return voteClear, true
		}
	case voteDown:
		if m.dislikedTracks[id] {
			return voteClear, true
		}
	}
	return desired, true
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
