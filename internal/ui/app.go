// Package ui is the entire Bubble Tea presentation layer for addiplay.
//
// File layout follows the screen-per-file idiom:
//
//	app.go            — root Model + Init/Update/View dispatch
//	screen_home.go    — primary screen: header + (channels | now-playing) + status
//	screen_network.go — network-picker overlay
//	screen_search.go  — search/filter overlay
//	screen_login.go   — credential entry overlay (shown on first run / 401)
//	channels.go       — card-based channel list renderer
//	nowplaying.go     — right-pane art + track info
//	header.go         — top bar
//	status.go         — bottom status bar
//	theme.go          — per-network palettes + lipgloss styles
//	keymap.go         — global key bindings
//	orchestrate.go    — Cmd/Msg wiring to domain packages
package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/config"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/fanart"
	"github.com/dimmkirr/addiplay/internal/player"
)

// Focus indicates which screen mode is active. The default is FocusChannels
// (the home screen); the other values are overlays that capture key input.
type Focus int

const (
	FocusChannels Focus = iota
	FocusNetworkPicker
	FocusSearch
	FocusLogin
	// FocusHelp is the keymap overlay (DIMM-392) — opened by `?`,
	// closed by `?`/Esc/`q`. The header has always advertised "[?] keys";
	// before DIMM-392 the binding didn't exist, making the hint a
	// dead promise. Lives behind a focus mode so it works from any
	// non-input screen and self-renders without a separate Update loop.
	FocusHelp
)

// Tab is the right-pane filter tab.
type Tab int

const (
	TabAll Tab = iota
	TabFavorites
)

// AudioClient is the audioaddict surface the UI uses. The production
// implementation is *audioaddict.Client; tests / demo use a fake.
type AudioClient interface {
	Authenticate(ctx context.Context, email, password, network string) (audioaddict.Member, error)
	Channels(ctx context.Context, network string) ([]audioaddict.Channel, error)
	StreamURL(ctx context.Context, network, channel string, q audioaddict.Quality) (string, error)
	CurrentlyPlaying(ctx context.Context, network string, channelID int64) (audioaddict.Track, error)
	LikeTrack(ctx context.Context, network string, trackID, channelID int64) error
	DislikeTrack(ctx context.Context, network string, trackID, channelID int64) error
	UnlikeTrack(ctx context.Context, network string, trackID, channelID int64) error
	// FetchTrack reads /tracks/<id> and carries the bloom filters used
	// to detect whether the current member has voted on this track.
	FetchTrack(ctx context.Context, network string, trackID int64) (*audioaddict.TrackInfo, error)
	// SetCreds + Creds let the UI push a freshly-loaded Session into the
	// client on startup and read back the current state for the header.
	SetCreds(s creds.Session)
	Creds() creds.Session
	Logout() error
}

// AudioPlayer is the playback surface the UI uses. Production is
// *player.Player; demo provides a no-op fake that still emits state events.
type AudioPlayer interface {
	Play(url string) error
	Pause() error
	Resume() error
	Stop() error
	SetVolume(pct int) error
	Close() error
	Events() <-chan player.Event
	State() player.State
}

// NewPlayerFunc constructs the player; called once at Init() so failures
// surface as a UI toast rather than a startup crash.
type NewPlayerFunc func(ctx context.Context) (AudioPlayer, error)

// Model is the Bubble Tea root model. Screen-specific behaviour lives in
// screen_*.go files; this struct is the single source of truth for state
// they read and mutate.
type Model struct {
	// ctx is the cancellable context every Cmd derives its HTTP timeouts
	// from. Cancelling it aborts in-flight fanart, channel-thumb, track,
	// and login fetches immediately — the "freeze on exit" symptom was
	// the Go runtime waiting on 5+ pending HTTP fetches to hit their 10s
	// timeouts after the user pressed `q`. The cancel func lives on the
	// model so the quit handler can fire it before returning tea.Quit.
	ctx       context.Context
	cancel    context.CancelFunc
	creds     creds.Session
	client    AudioClient
	// pendingVote remembers the vote the user attempted right before
	// `voteRequest` returned ErrSessionInvalid; replayed automatically on
	// the next loginSuccessMsg. Avoids making the user press `l` twice.
	pendingVote *pendingVote
	player    AudioPlayer
	newPlayer NewPlayerFunc
	cfg       config.Config

	theme  Theme
	st     styles
	width  int
	height int

	channels []audioaddict.Channel
	selIdx   int
	tab      Tab

	currentNetwork string // network the channel-list pane is BROWSING
	playingNetwork string // network of the channel currently being PLAYED
	currentChannel string
	currentTrack   audioaddict.Track

	focus       Focus
	prevFocus   Focus // remembered when opening FocusHelp so Esc restores it
	searchInput textinput.Model
	netCursor   int
	// pendingLogout (DIMM-393) is set on the FIRST `L` press; only the
	// SECOND press within `logoutConfirmWindow` actually wipes the
	// session. `L` is one shift-key from `l` (like) so we make the
	// destructive action confirm-then-act instead of fire-and-forget.
	// Esc / any non-`L` key clears it; a Tick clears it after the
	// window expires so the toast doesn't lie.
	pendingLogout bool

	// Login overlay inputs (FocusLogin). Initialized lazily; see screen_login.go.
	loginEmail    textinput.Model
	loginPassword textinput.Model
	loginField    int    // 0 = email, 1 = password, 2 = submit
	loginError    string // last auth failure, cleared on next submit
	loginBusy     bool   // an Authenticate request is in flight

	toast     string
	loading   bool
	resolving bool
	playerSt  player.State

	trackTickGen uint64

	// Fanart for the currently-playing channel/track.
	fanartEscape    string
	fanartSourceURL string
	fanartID        uint32
	fanartCache     *fanart.Cache

	// Per-channel ASCII thumbnails for the card list. Keyed on channel.Key
	// (stable across navigations); populated lazily as cards enter the
	// viewport. Empty value means "fetch in flight" so we don't enqueue
	// duplicates. Empty map means "thumbnails disabled" (fanart mode is
	// None, e.g. headless / non-truecolor terminal).
	channelThumbs map[string]string

	// likedTracks is the session-local set of track IDs the user has
	// upvoted via the `l` key. We don't fetch prior likes from the server
	// (no GET endpoint researched — DIMM-381 deferred).
	likedTracks map[int64]bool
	// dislikedTracks is the mirror for the `s` key (DIMM-382). Mutually
	// exclusive with likedTracks per server semantics: POST /up clears a
	// /down vote and vice-versa.
	dislikedTracks map[int64]bool
	// voteInFlight debounces double-presses while a like/dislike/unlike
	// request is mid-flight.
	voteInFlight bool
}

// sliceToMap converts a persisted []int64 vote list to the runtime
// set-shaped map used by the UI.
func sliceToMap(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id != 0 {
			m[id] = true
		}
	}
	return m
}

// mapToSlice flattens the runtime set back to a deterministic slice
// for YAML persistence (sorted so config-file diffs stay small across
// runs that vote on tracks in different orders).
func mapToSlice(m map[int64]bool) []int64 {
	if len(m) == 0 {
		return nil
	}
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// NewModel constructs the root model with explicit client and player
// constructor. cmd/tui.go injects the real ones; cmd/demo.go injects fakes.
func NewModel(ctx context.Context, c creds.Session, client AudioClient, newPlayer NewPlayerFunc) Model {
	cfg, _ := config.Load()
	theme := ThemeFor(cfg.LastNetwork)

	ti := textinput.New()
	ti.Placeholder = "filter channels…"
	ti.CharLimit = 64
	ti.Prompt = "/ "

	// Wrap the incoming context so we own a cancel handle. updateHome's
	// quit branch calls m.cancel() before returning tea.Quit so every
	// in-flight HTTP fetch aborts immediately instead of blocking the
	// process for up to 10s while goroutines drain.
	ctx, cancel := context.WithCancel(ctx)
	m := Model{
		ctx:            ctx,
		cancel:         cancel,
		creds:          c,
		client:         client,
		newPlayer:      newPlayer,
		cfg:            cfg,
		theme:          theme,
		st:             newStyles(theme),
		currentNetwork: cfg.LastNetwork,
		focus:          FocusChannels,
		searchInput:    ti,
		fanartCache:    fanart.NewCache(),
		channelThumbs:  map[string]string{},
		likedTracks:    sliceToMap(cfg.LikedTracks),
		dislikedTracks: sliceToMap(cfg.DislikedTracks),
	}
	// Auto-show login overlay on first run / when creds are absent. The
	// caller (cmd/tui.go) usually checks first and skips constructing
	// the TUI without creds; this is the safety net for code paths
	// (demo, tests) that pass empty creds.
	if strings.TrimSpace(c.Email) == "" || strings.TrimSpace(c.ListenKey) == "" {
		m = m.initLoginInputs(true)
	}
	dlog("NewModel ready (email=%s lastNet=%s lastCh=%s focus=%d)",
		c.Email, cfg.LastNetwork, cfg.LastChannel, m.focus)
	return m
}

// Init returns the bootstrap command — start the player and load channels.
func (m Model) Init() tea.Cmd {
	dlog("Init dispatching initPlayer + loadChannels(net=%s)", m.currentNetwork)
	return tea.Batch(initPlayerCmd(m.ctx, m.newPlayer), loadChannelsCmd(m.ctx, m.client, m.currentNetwork))
}

// Update is the single entry-point. It first handles domain events (player,
// channel-load, stream, fanart, etc.) regardless of focus, then routes
// keyboard input to the active screen's handler.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Key dispatch: route to the screen that owns the current focus.
	if k, ok := msg.(tea.KeyMsg); ok {
		// Redact runes when the user is typing into a credential field —
		// otherwise an `l`-to-vote keystroke trace would also capture
		// the password (literal) and email (PII) as they're typed.
		// We still log type/focus/loginField + the rune COUNT so the
		// shape of input is debuggable.
		runes := string(k.Runes)
		str := k.String()
		if m.focus == FocusLogin && (m.loginField == 0 || m.loginField == 1) && k.Type == tea.KeyRunes {
			n := len(k.Runes)
			runes = fmt.Sprintf("<redacted:n=%d>", n)
			str = fmt.Sprintf("<redacted:n=%d>", n)
		}
		dlog("key: type=%s alt=%t runes=%q str=%q focus=%d loginField=%d loginBusy=%t loginError=%q currentTrack.ID=%d voteInFlight=%t session_key_len=%d",
			k.Type, k.Alt, runes, str,
			m.focus, m.loginField, m.loginBusy, m.loginError,
			m.currentTrack.ID, m.voteInFlight, len(m.creds.SessionKey))
		switch m.focus {
		case FocusSearch:
			return m.updateSearch(k)
		case FocusNetworkPicker:
			return m.updateNetworkPicker(k)
		case FocusLogin:
			return m.updateLogin(k)
		case FocusHelp:
			return m.updateHelp(k)
		default:
			return m.updateHome(k)
		}
	}
	return m.handleDomain(msg)
}

// handleDomain processes non-key messages (player events, network/stream
// callbacks, fanart). The same routing happens regardless of focus —
// every screen reflects the same underlying playback/network state.
func (m Model) handleDomain(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case playerReadyMsg:
		m.player = msg.p
		cmds = append(cmds, pumpPlayerEventsCmd(m.player))

	case playerErrorMsg:
		m.toast = msg.err.Error()
		m.loading = false

	case playerStateMsg:
		m.playerSt = msg.state
		m.loading = msg.state == player.StateLoading
		if msg.state == player.StatePlaying {
			m.toast = ""
		}
		cmds = append(cmds, pumpPlayerEventsCmd(m.player))

	case channelsLoadedMsg:
		dlog("channelsLoaded count=%d net=%s", len(msg.channels), m.currentNetwork)
		m.channels = msg.channels
		m.selIdx = 0
		// Clear the previous network's thumbnail cache — keys collide
		// across networks (e.g. "classictrance" exists on both di and
		// radiotunes with different art).
		m.channelThumbs = map[string]string{}
		// Auto-resume the last-played channel — but ONLY when nothing
		// is currently playing on this network. Otherwise a mid-session
		// reload (e.g. after re-auth) would yank mpv off the active
		// track even though the listen_key hasn't changed.
		alreadyPlaying := m.currentChannel != "" && m.playingNetwork == m.currentNetwork
		if !alreadyPlaying && m.cfg.LastChannel != "" && m.player != nil {
			for i, ch := range m.channels {
				if ch.Key == m.cfg.LastChannel {
					m.selIdx = i
					cmds = append(cmds, playSelectedCmd(m.ctx, m.client, m.player, m.currentNetwork, ch))
					m.currentChannel = ch.Key
					break
				}
			}
		} else if alreadyPlaying {
			// Keep the visual selection in sync with what's actually playing.
			for i, ch := range m.channels {
				if ch.Key == m.currentChannel {
					m.selIdx = i
					break
				}
			}
		}
		// Prefetch thumbnails for the initial viewport so cards aren't
		// blank when the screen first paints.
		cmds = append(cmds, m.kickoffVisibleThumbs()...)

	case channelThumbReadyMsg:
		// Empty escape signals a fetch error — drop the in-flight marker
		// so a later scroll back can retry, but don't cache a bad value.
		if msg.escape == "" {
			dlog("channelThumb FAIL key=%s (cache slot freed)", msg.key)
			delete(m.channelThumbs, msg.key)
			break
		}
		dlog("channelThumb OK key=%s bytes=%d", msg.key, len(msg.escape))
		m.channelThumbs[msg.key] = msg.escape

	case channelsErrorMsg:
		if msg.unauthorized {
			m.toast = "session expired — sign in again"
			// Auto-pop the login overlay so the user can recover in-place.
			m = m.initLoginInputs(false)
		} else {
			m.toast = "load channels: " + msg.err.Error()
		}

	case streamPlayingMsg:
		m.resolving = false
		m.currentChannel = msg.channel.Key
		m.playingNetwork = msg.network
		m.currentNetwork = msg.network
		m.cfg.LastChannel = msg.channel.Key
		m.cfg.LastNetwork = msg.network
		_ = m.cfg.Save()
		m.trackTickGen++
		gen := m.trackTickGen
		cmds = append(cmds,
			fetchTrackCmd(m.ctx, m.client, m.playingNetwork, msg.channel.ID, gen),
			tickTrackCmd(m.ctx, m.client, m.playingNetwork, msg.channel.ID, gen),
		)
		if cmd := m.refreshFanart(audioaddict.Track{}, msg.channel); cmd != nil {
			cmds = append(cmds, cmd)
		}

	case fanartReadyMsg:
		// Drop late arrivals: source URL changed since this fetch was
		// dispatched, so painting it would be a regression.
		if msg.url != m.fanartSourceURL {
			dlog("fanartReady DROPPED (url changed) url=%s want=%s", msg.url, m.fanartSourceURL)
			break
		}
		// Refuse to overwrite valid art with a fetch failure — leave the
		// stale image (channel art is better than placeholder). Toast
		// once so the user knows when album art genuinely 404s / decodes
		// wrong, instead of silently showing the channel image forever.
		if msg.err != nil || msg.escape == "" {
			dlog("fanartReady FAIL url=%s err=%v", msg.url, msg.err)
			if msg.err != nil && m.toast == "" {
				m.toast = "art fetch: " + msg.err.Error()
			}
			break
		}
		dlog("fanartReady APPLIED url=%s bytes=%d", msg.url, len(msg.escape))
		m.fanartEscape = msg.escape

	case streamErrorMsg:
		m.resolving = false
		switch {
		case msg.listenKeyDead:
			// listen_key rejected at the stream resolver — full re-auth
			// is required (the credential the URL is signed with is dead).
			m.toast = "listen_key rejected — sign in again"
			m = m.initLoginInputs(false)
		case msg.unauthorized:
			m.toast = "session expired — sign in again"
			m = m.initLoginInputs(false)
		default:
			m.toast = "play: " + msg.err.Error()
		}
		m.loading = false

	case trackUpdateMsg:
		if msg.gen != m.trackTickGen {
			break
		}
		if msg.channelID == channelIDFromKey(m.channels, m.currentChannel) || m.playingNetwork != m.currentNetwork {
			// Vote restoration: on track change, fetch the track's
			// bloom filter and check whether this member is in the
			// who_upvoted / who_downvoted set. Hash function is
			// `crc32(memberID + ":" + (i+seed))` per di.fm's web
			// player — reverse-engineered 2026-06-29 (DIMM-383
			// follow-up). Local config mirror (config.LikedTracks)
			// remains for first-paint state before this resolves AND
			// for tracks where the API request fails.
			newTrackID := msg.track.ID
			loadVote := newTrackID != 0 && newTrackID != m.currentTrack.ID && m.creds.ID != 0
			m.currentTrack = msg.track
			ch, _ := channelByID(m.channels, msg.channelID)
			if cmd := m.refreshFanart(msg.track, ch); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if loadVote {
				cmds = append(cmds, loadVoteStateCmd(m.ctx, m.client, m.playingNetwork, newTrackID, m.creds.ID))
			}
			cmds = append(cmds, tickTrackCmd(m.ctx, m.client, m.playingNetwork, msg.channelID, msg.gen))
		}

	case trackVoteLoadedMsg:
		dlog("trackVoteLoadedMsg: trackID=%d liked=%t disliked=%t", msg.trackID, msg.liked, msg.disliked)
		if m.likedTracks == nil {
			m.likedTracks = map[int64]bool{}
		}
		if m.dislikedTracks == nil {
			m.dislikedTracks = map[int64]bool{}
		}
		switch {
		case msg.liked:
			m.likedTracks[msg.trackID] = true
			delete(m.dislikedTracks, msg.trackID)
		case msg.disliked:
			m.dislikedTracks[msg.trackID] = true
			delete(m.likedTracks, msg.trackID)
		}
		// Sync to local config too — keeps the offline fallback warm
		// in case the API is unreachable on next launch.
		m.cfg.LikedTracks = mapToSlice(m.likedTracks)
		m.cfg.DislikedTracks = mapToSlice(m.dislikedTracks)
		_ = m.cfg.Save()

	case networkSwitchedMsg:
		m.currentNetwork = msg.network
		m.channels = nil
		m.selIdx = 0
		m.theme = ThemeFor(msg.network)
		m.st = newStyles(m.theme)
		m.cfg.LastNetwork = msg.network
		_ = m.cfg.Save()
		cmds = append(cmds, loadChannelsCmd(m.ctx, m.client, msg.network))

	case loginSuccessMsg:
		dlog("loginSuccessMsg: email_set=%t listen_key_len=%d session_key_len=%d premium=%t channels_loaded=%d currentChannel=%q pendingVote=%t",
			msg.creds.Email != "", len(msg.creds.ListenKey), len(msg.creds.SessionKey), msg.creds.Premium,
			len(m.channels), m.currentChannel, m.pendingVote != nil)
		m.creds = msg.creds
		m.focus = FocusChannels
		m.loginBusy = false
		m.loginError = ""
		// Clear any "session expired" toast left over from the 401/403
		// that popped the overlay — the fresh creds invalidate it.
		m.toast = ""
		// Re-load channels ONLY if we don't have them yet. Channel
		// listing is a public read — it doesn't use listen_key or
		// session_key, so the existing list (loaded at startup) is
		// still valid. The previous unconditional reload had two bad
		// side effects: (1) it fires channelsLoadedMsg → auto-resume
		// branch → playSelectedCmd, restarting the stream mid-track,
		// even though listen_key is unchanged and mpv is still happy;
		// (2) it dropped channelThumbs and refetched every thumbnail.
		if len(m.channels) == 0 {
			cmds = append(cmds, loadChannelsCmd(m.ctx, m.client, m.currentNetwork))
		}
		// Replay the vote that bounced off ErrSessionInvalid. The
		// in-memory + persisted session_key is now fresh, so the
		// retry should land. Cleared regardless of dispatch result
		// to prevent a runaway loop on persistent 403.
		if m.pendingVote != nil {
			pv := m.pendingVote
			m.pendingVote = nil
			dlog("loginSuccessMsg: replaying pendingVote (network=%s track=%d channel=%d dir=%d)",
				pv.network, pv.trackID, pv.channelID, pv.dir)
			m.voteInFlight = true
			cmds = append(cmds, voteCmd(m.ctx, m.client, pv.network, pv.trackID, pv.channelID, pv.dir))
		}

	case loginErrorMsg:
		dlog("loginErrorMsg: %v", msg.err)
		m.loginBusy = false
		m.loginError = msg.err.Error()

	case logoutConfirmTimeoutMsg:
		// 3s expired after first `L` — clear the pending state so the
		// toast doesn't lie. No-op if a second `L` already fired.
		if m.pendingLogout {
			dlog("logoutConfirmTimeoutMsg: clearing stale pendingLogout")
			m.pendingLogout = false
			if strings.HasPrefix(m.toast, "press L") {
				m.toast = ""
			}
		}

	case voteOKMsg:
		dlog("voteOKMsg: trackID=%d liked=%t disliked=%t — vote applied", msg.trackID, msg.liked, msg.disliked)
		if m.likedTracks == nil {
			m.likedTracks = map[int64]bool{}
		}
		if m.dislikedTracks == nil {
			m.dislikedTracks = map[int64]bool{}
		}
		switch {
		case msg.liked:
			m.likedTracks[msg.trackID] = true
			delete(m.dislikedTracks, msg.trackID)
		case msg.disliked:
			m.dislikedTracks[msg.trackID] = true
			delete(m.likedTracks, msg.trackID)
		default:
			delete(m.likedTracks, msg.trackID)
			delete(m.dislikedTracks, msg.trackID)
		}
		m.voteInFlight = false
		// Persist for next launch — the bloom filter on the API side
		// is opaque, so we keep a local mirror of what we've voted on.
		m.cfg.LikedTracks = mapToSlice(m.likedTracks)
		m.cfg.DislikedTracks = mapToSlice(m.dislikedTracks)
		if err := m.cfg.Save(); err != nil {
			dlog("voteOKMsg: cfg.Save FAIL err=%v", err)
		}

	case voteErrMsg:
		m.voteInFlight = false
		switch {
		case msg.sessionInvalid:
			// The X-Session-Key was rejected. Stash the original op so
			// loginSuccessMsg replays it; pop login. Toast is empty —
			// the overlay is enough signal; the user just typed `l` and
			// gets the password prompt straight away.
			dlog("voteErrMsg: sessionInvalid — stashing pendingVote and popping login")
			m.pendingVote = &pendingVote{
				network:   msg.network,
				trackID:   msg.trackID,
				channelID: msg.channelID,
				dir:       msg.dir,
			}
			m.toast = ""
			m = m.initLoginInputs(false)
		case msg.unauthorized:
			// Generic 401/403 — full re-auth path. (Today this should
			// not fire on vote since ErrSessionInvalid covers it; kept
			// as defense in depth.)
			m.toast = "session expired — sign in again"
			m = m.initLoginInputs(false)
		default:
			m.toast = "vote: " + msg.err.Error()
		}
	}

	return m, tea.Batch(cmds...)
}

// View dispatches to the active screen's renderer. Overlays (network
// picker, login) replace the whole frame; search shares the home screen
// because its input is rendered inline as the channel-list header.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	switch m.focus {
	case FocusNetworkPicker:
		return m.viewNetworkPicker()
	case FocusLogin:
		return m.viewLogin()
	case FocusHelp:
		return m.viewHelp()
	default:
		return m.viewHome()
	}
}
