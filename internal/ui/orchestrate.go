package ui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/fanart"
	"github.com/dimmkirr/addiplay/internal/player"
)

// -----------------------------------------------------------------------------
// Messages
// -----------------------------------------------------------------------------

type (
	playerReadyMsg    struct{ p AudioPlayer }
	playerErrorMsg    struct{ err error }
	playerStateMsg    struct{ state player.State }
	channelsLoadedMsg struct{ channels []audioaddict.Channel }
	channelsErrorMsg struct {
		err          error
		unauthorized bool
	}
	streamPlayingMsg struct {
		network string
		channel audioaddict.Channel
	}
	streamErrorMsg struct {
		err            error
		unauthorized   bool
		listenKeyDead  bool // true on ErrListenKeyRejected — needs full re-auth
	}
	trackUpdateMsg struct {
		channelID int64
		track     audioaddict.Track
		gen       uint64 // matches Model.trackTickGen at time of dispatch
	}
	networkSwitchedMsg struct{ network string }
	// fanartReadyMsg carries a freshly-encoded escape OR an err to signal
	// that the fetch failed (so the handler can keep stale art visible
	// rather than wiping it to a placeholder).
	fanartReadyMsg struct {
		url, escape string
		err         error
	}

	// trackVoteLoadedMsg carries the result of looking up the current
	// user's vote state for a track via FetchTrack + bloom-filter check.
	// Only emitted when there's a non-zero answer (liked or disliked);
	// neutral results don't need a message because the maps are already
	// keyed by "absent = neutral".
	trackVoteLoadedMsg struct {
		trackID  int64
		liked    bool
		disliked bool
	}

	// channelThumbReadyMsg carries a fetched-and-encoded ASCII thumbnail
	// for one channel's left-of-card swatch. key == channel.Key (not ID:
	// IDs differ per network but Key is stable within a network and the
	// cache is wiped on network switch — see channelsLoadedMsg handler).
	// Empty escape signals a fetch error; the handler removes the in-
	// flight marker so a later scroll can retry.
	channelThumbReadyMsg struct{ key, escape string }

	// voteOKMsg is dispatched after a successful LikeTrack/DislikeTrack/
	// UnlikeTrack call. The handler sets the matching map and clears the
	// other — `liked` and `disliked` are mutually exclusive; both false
	// means the vote was cleared (DELETE).
	voteOKMsg struct {
		trackID  int64
		liked    bool
		disliked bool
	}
	// voteErrMsg surfaces a vote-API failure. `sessionInvalid=true` means
	// the X-Session-Key was rejected (ErrSessionInvalid) — the UI pops
	// the login overlay AND remembers the attempted op as a pendingVote
	// so it replays on the next loginSuccessMsg. Generic `unauthorized`
	// kept for back-compat with the channels/stream paths.
	voteErrMsg struct {
		err            error
		unauthorized   bool
		sessionInvalid bool
		// Original op — replayed verbatim after re-auth.
		network   string
		trackID   int64
		channelID int64
		dir       voteDirection
	}
)

// pendingVote captures a vote attempt that bounced off ErrSessionInvalid,
// so loginSuccessMsg can replay it without the user pressing `l` twice.
type pendingVote struct {
	network   string
	trackID   int64
	channelID int64
	dir       voteDirection
}

// -----------------------------------------------------------------------------
// Commands
// -----------------------------------------------------------------------------

// initPlayerCmd starts the player via the injected constructor.
func initPlayerCmd(ctx context.Context, newPlayer NewPlayerFunc) tea.Cmd {
	return func() tea.Msg {
		p, err := newPlayer(ctx)
		if err != nil {
			return playerErrorMsg{err: err}
		}
		return playerReadyMsg{p: p}
	}
}

// pumpPlayerEventsCmd reads a single event from the player and re-arms.
// Returning nil ends the chain when the player is closed.
func pumpPlayerEventsCmd(p AudioPlayer) tea.Cmd {
	if p == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-p.Events()
		if !ok {
			return nil
		}
		if ev.Err != nil {
			return playerErrorMsg{err: ev.Err}
		}
		return playerStateMsg{state: ev.State}
	}
}

// loadChannelsCmd fetches channels for the network.
func loadChannelsCmd(ctx context.Context, client AudioClient, network string) tea.Cmd {
	return func() tea.Msg {
		channels, err := client.Channels(ctx, network)
		if err != nil {
			return channelsErrorMsg{err: err, unauthorized: errors.Is(err, audioaddict.ErrUnauthorized)}
		}
		return channelsLoadedMsg{channels: channels}
	}
}

// playSelectedCmd resolves a stream URL and tells the player to play it.
// listen_key comes from the client's stored Session — no caller threads it.
func playSelectedCmd(ctx context.Context, client AudioClient, p AudioPlayer, network string, ch audioaddict.Channel) tea.Cmd {
	return func() tea.Msg {
		url, err := client.StreamURL(ctx, network, ch.Key, audioaddict.QualityPremiumHigh)
		if err != nil {
			return streamErrorMsg{
				err:           err,
				unauthorized:  errors.Is(err, audioaddict.ErrUnauthorized),
				listenKeyDead: errors.Is(err, audioaddict.ErrListenKeyRejected),
			}
		}
		if err := p.Play(url); err != nil {
			return streamErrorMsg{err: err}
		}
		return streamPlayingMsg{network: network, channel: ch}
	}
}

// fetchTrackCmd fetches the currently-playing track once, immediately.
// Used right after playback starts so the status bar shows the track without
// waiting 15s for the periodic ticker. `gen` is captured so the handler can
// drop stale results when the user has since switched channels.
//
// parent is m.ctx — when the user quits, parent cancels and the
// in-flight CurrentlyPlaying HTTP returns immediately instead of
// blocking the process for its full 5s timeout.
func fetchTrackCmd(parent context.Context, client AudioClient, network string, channelID int64, gen uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		track, err := client.CurrentlyPlaying(ctx, network, channelID)
		if err != nil {
			return nil
		}
		return trackUpdateMsg{channelID: channelID, track: track, gen: gen}
	}
}

// tickTrackCmd schedules a currently-playing refresh ~every 15s. `gen` is
// captured so each tick carries the generation it was queued under; the
// handler drops ticks whose gen is stale, naturally retiring the chain
// when the user switches channels. parent is m.ctx — see fetchTrackCmd.
func tickTrackCmd(parent context.Context, client AudioClient, network string, channelID int64, gen uint64) tea.Cmd {
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		track, err := client.CurrentlyPlaying(ctx, network, channelID)
		if err != nil {
			return nil
		}
		return trackUpdateMsg{channelID: channelID, track: track, gen: gen}
	})
}

// voteDirection picks which AudioAddict vote endpoint a voteCmd hits.
// Maps 1:1 onto the three accepted server values (`up`/`down`/`delete`).
type voteDirection int

const (
	voteUp    voteDirection = iota // POST .../vote/<chid>/up
	voteDown                       // POST .../vote/<chid>/down
	voteClear                      // DELETE .../vote/<chid>
)

// voteCmd issues the like/dislike/clear request against the AudioAddict
// API. session_key is owned by the Client now — no caller threads it.
// The caller sets voteInFlight before dispatch and clears it on the
// resulting voteOKMsg/voteErrMsg. The voteErrMsg carries the original
// op (network/trackID/channelID/dir) so a session-invalid error can be
// stashed as pendingVote and replayed after re-auth.
func voteCmd(ctx context.Context, client AudioClient, network string, trackID, channelID int64, dir voteDirection) tea.Cmd {
	return func() tea.Msg {
		var err error
		switch dir {
		case voteUp:
			err = client.LikeTrack(ctx, network, trackID, channelID)
		case voteDown:
			err = client.DislikeTrack(ctx, network, trackID, channelID)
		case voteClear:
			err = client.UnlikeTrack(ctx, network, trackID, channelID)
		}
		if err != nil {
			return voteErrMsg{
				err:            err,
				unauthorized:   errors.Is(err, audioaddict.ErrUnauthorized),
				sessionInvalid: errors.Is(err, audioaddict.ErrSessionInvalid),
				network:        network,
				trackID:        trackID,
				channelID:      channelID,
				dir:            dir,
			}
		}
		return voteOKMsg{
			trackID:  trackID,
			liked:    dir == voteUp,
			disliked: dir == voteDown,
		}
	}
}

// logoutConfirmTimeoutMsg is emitted ~3s after the FIRST `L` press
// (DIMM-393). The handler clears `m.pendingLogout` so the toast doesn't
// lie and a future `L` requires its own fresh confirmation.
type logoutConfirmTimeoutMsg struct{}

// logoutConfirmTimeoutCmd schedules the message.
func logoutConfirmTimeoutCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(_ time.Time) tea.Msg {
		return logoutConfirmTimeoutMsg{}
	})
}

// loadVoteStateCmd fetches the track's bloom-filter votes block and
// emits trackVoteLoadedMsg with the user's current up/down state. On
// fetch error, returns nil (silent failure — the heart just stays
// neutral instead of toasting a confusing error).
func loadVoteStateCmd(parent context.Context, client AudioClient, network string, trackID, memberID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		info, err := client.FetchTrack(ctx, network, trackID)
		if err != nil || info == nil {
			return nil
		}
		return trackVoteLoadedMsg{
			trackID:  trackID,
			liked:    info.Votes.WhoUpvoted.Contains(memberID),
			disliked: info.Votes.WhoDownvoted.Contains(memberID),
		}
	}
}

// switchNetworkCmd emits a synchronous network switch.
func switchNetworkCmd(network string) tea.Cmd {
	return func() tea.Msg {
		return networkSwitchedMsg{network: network}
	}
}

// fetchChannelThumbCmd downloads the channel's image and encodes it for
// the card's left swatch (cols × rows cells). Dispatches by fanart mode:
//
//   - Kitty: uses fanart.FetchPlaceholder which emits an a=T,U=1 virtual-
//     placement transmit + a U+10EEEE placeholder block. The placeholders
//     measure as 1 cell wide each, so lipgloss.JoinHorizontal layouts
//     stay aligned while the terminal overlays the real raster image.
//     Each card needs a UNIQUE image id (the `id` param); collisions
//     would cause one image to overwrite another.
//   - ASCII: half-block (▀) text rendering. Works in any truecolor
//     terminal including tmux, doesn't need a per-cell id.
//
// Silently no-ops (returns empty escape) when the channel has no usable
// image URL or the fetch fails — the renderer falls back to a colored
// placeholder so the card still draws.
func fetchChannelThumbCmd(ctx context.Context, channelKey, imageURL string, cols, rows int, mode fanart.Mode, id uint32) tea.Cmd {
	return func() tea.Msg {
		if imageURL == "" {
			return channelThumbReadyMsg{key: channelKey, escape: ""}
		}
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var (
			esc string
			err error
		)
		switch mode {
		case fanart.ModeKitty:
			esc, err = fanart.FetchPlaceholder(fctx, imageURL, cols, rows, id)
		case fanart.ModeASCII:
			esc, err = fanart.FetchASCII(fctx, imageURL, cols, rows)
		default:
			return channelThumbReadyMsg{key: channelKey, escape: ""}
		}
		if err != nil {
			return channelThumbReadyMsg{key: channelKey, escape: ""}
		}
		return channelThumbReadyMsg{key: channelKey, escape: esc}
	}
}

// fetchFanartCmd downloads + encodes the channel image off the UI thread,
// then caches and reports back via fanartReadyMsg. Picks the right encoder
// (Kitty graphics vs ASCII half-blocks) based on the active fanart mode.
// Silently no-ops on error so a missing image never breaks the TUI.
func fetchFanartCmd(ctx context.Context, cache *fanart.Cache, url string, cols, rows int, id uint32, mode fanart.Mode) tea.Cmd {
	return func() tea.Msg {
		fctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		var (
			escape string
			err    error
		)
		switch mode {
		case fanart.ModeKitty:
			// Use the Unicode-placeholder protocol so lipgloss measures
			// the image as `rows × cols` cells (instead of 0 — the raw
			// `a=T` escape has no visible width to lipgloss). Without
			// this, label/tagline/track text would be written at screen
			// rows 2-4 while Kitty rendered the image at rows 1-15,
			// covering the text. Same id namespace as before
			// (nowPlayingFanartID = 1 for Now Playing).
			escape, err = fanart.FetchPlaceholder(fctx, url, cols, rows, id)
		case fanart.ModeASCII:
			escape, err = fanart.FetchASCII(fctx, url, cols, rows)
		default:
			return nil
		}
		if err != nil {
			// Surface the failure so the model handler can decide what
			// to do (e.g. keep showing stale art, toast a warning).
			// Returning nil here was the source of the "album never shows"
			// symptom — the old escape stuck around or was wiped to a
			// placeholder with no clue why.
			return fanartReadyMsg{url: url, err: err}
		}
		cache.Put(url, escape)
		return fanartReadyMsg{url: url, escape: escape}
	}
}
