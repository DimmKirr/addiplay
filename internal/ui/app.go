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
	"time"

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
	FocusAbout
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
	SkipTrack(ctx context.Context, network string, trackID, channelID int64, trackLength, skippedAt int) (*audioaddict.SkipResponse, error)
	FetchRoutine(ctx context.Context, network string, channelID int64, tuneIn bool) (*audioaddict.RoutineResult, error)
	// FetchTrack reads /tracks/<id> and carries the bloom filters used
	// to detect whether the current member has voted on this track.
	FetchTrack(ctx context.Context, network string, trackID int64) (*audioaddict.TrackInfo, error)
	ChannelHistory(ctx context.Context, network string, channelID int64) ([]audioaddict.Track, error)
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
	SetTrackMetadata(artist, title string) error
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
	// pendingSkip mirrors pendingVote for skip attempts that bounced off
	// ErrSessionInvalid — replayed after re-auth so the user doesn't have
	// to press skip twice. (DIMM-423)
	pendingSkip *pendingSkip
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

	recentTracks      []audioaddict.Track
	trackStartTime    time.Time
	trackPauseElapsed time.Duration
	voteUp            int
	voteDown          int

	focus       Focus
	prevFocus   Focus // remembered when opening FocusHelp/FocusAbout so Esc restores it
	aboutScroll int   // scroll offset for the About screen
	aboutCursor int   // selected color index (0-255) in the palette
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

	toast       string
	toastIsWarn bool
	statusInfo  string // transient info shown inline in the now-playing line (green)
	loading     bool
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
	// dislikedTracks is the mirror for the `d` key (DIMM-382). Mutually
	// exclusive with likedTracks per server semantics: POST /up clears a
	// /down vote and vice-versa.
	dislikedTracks map[int64]bool
	// voteInFlight debounces double-presses while a like/dislike/unlike
	// request is mid-flight.
	voteInFlight bool
	// skipInFlight debounces `s` while a skip request is mid-flight.
	skipInFlight bool
	// trackQueue holds on-demand tracks for the current channel (per-track
	// mode via channel_routine API). nil means live-stream mode.
	trackQueue *audioaddict.TrackQueue
	// sessionExpiresAt is the parsed expires_on from the last routine
	// response. Within this window we trust the cached auth and treat
	// API failures as transient network errors rather than session death.
	sessionExpiresAt time.Time
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

func (m Model) channelIDForKey(key string) int64 {
	for _, ch := range m.channels {
		if ch.Key == key {
			return ch.ID
		}
	}
	return 0
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
	return tea.Batch(initPlayerCmd(m.ctx, m.newPlayer), loadChannelsCmd(m.ctx, m.client, m.currentNetwork), sessionCheckCmd(m.ctx, m.client, m.currentNetwork))
}

// Update is the single entry-point. It first handles domain events (player,
// channel-load, stream, fanart, etc.) regardless of focus, then routes
// keyboard input to the active screen's handler.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Mouse dispatch: handle clicks and scroll wheel.
	if mm, ok := msg.(tea.MouseMsg); ok {
		switch m.focus {
		case FocusChannels:
			return m.handleMouse(mm)
		case FocusAbout:
			return m.handleAboutMouse(mm)
		}
		return m, nil
	}

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
		case FocusAbout:
			return m.updateAbout(k)
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
	case tea.FocusMsg:
		dlog("FocusMsg: terminal gained focus — clearing fanartEscape to force Kitty re-render (src=%q escapeLen=%d)", m.fanartSourceURL, len(m.fanartEscape))
		m.fanartEscape = ""
		cmds = append(cmds, func() tea.Msg { return fanartRefreshMsg{} })

	case fanartRefreshMsg:
		ch := m.playingChannel()
		if ch.Key != "" {
			dlog("fanartRefreshMsg: re-populating fanart from cache (ch=%q track=%q)", ch.Key, m.currentTrack.Title)
			if cmd := m.refreshFanart(m.currentTrack, ch); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if len(m.channels) > 0 {
			cmds = append(cmds, m.kickoffVisibleThumbs()...)
		}

	case playerReadyMsg:
		m.player = msg.p
		cmds = append(cmds, pumpPlayerEventsCmd(m.player))

	case playerErrorMsg:
		dlog("playerErrorMsg: %v", msg.err)
		m.toast = msg.err.Error()
		m.loading = false
		cmds = append(cmds, pumpPlayerEventsCmd(m.player))

	case mpvMetadataChangedMsg:
		dlog("mpvMetadataChanged: stream metadata changed (ICY title update)")
		if m.trackQueue == nil && m.currentChannel != "" {
			chID := channelIDFromKey(m.channels, m.currentChannel)
			if chID != 0 {
				cmds = append(cmds, fetchTrackCmd(m.ctx, m.client, m.playingNetwork, chID, m.trackTickGen))
			}
		}
		cmds = append(cmds, pumpPlayerEventsCmd(m.player))

	case mpvMediaTitleMsg:
		dlog("mpvMediaTitle: mpv reports media-title=%q", msg.title)
		cmds = append(cmds, pumpPlayerEventsCmd(m.player))

	case playerStateMsg:
		prevSt := m.playerSt
		m.playerSt = msg.state
		dlog("playerStateMsg: %s → %s", prevSt, msg.state)
		m.loading = msg.state == player.StateLoading
		if msg.state == player.StatePlaying {
			m.toast = ""
			cmds = append(cmds, progressTickCmd())
			if prevSt == player.StatePaused && m.trackPauseElapsed > 0 {
				m.trackStartTime = time.Now().Add(-m.trackPauseElapsed)
				m.trackPauseElapsed = 0
			}
		}
		if msg.state == player.StatePaused && prevSt == player.StatePlaying {
			m.trackPauseElapsed = time.Since(m.trackStartTime)
		}
		if msg.state == player.StateIdle {
			if m.trackQueue == nil {
				dlog("playerStateMsg: idle but trackQueue is nil — no auto-advance")
			} else if m.currentChannel == "" {
				dlog("playerStateMsg: idle but currentChannel is empty — no auto-advance")
			} else {
				chID := channelIDFromKey(m.channels, m.currentChannel)
				ch, ok := channelByID(m.channels, chID)
				if ok {
					dlog("playerStateMsg: idle — auto-advancing (ch=%s remaining=%d)", ch.Key, m.trackQueue.Remaining())
					cmds = append(cmds, advanceTrackCmd(m.ctx, m.client, m.player, m.playingNetwork, ch, m.trackQueue))
				} else {
					dlog("playerStateMsg: idle but channelByID(%d) not found in %d channels — no auto-advance", chID, len(m.channels))
				}
			}
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
			if m.creds.Email != "" && m.creds.Password != "" {
				dlog("channelsErrorMsg: unauthorized — attempting auto-renew")
				m.toast = "session refreshing…"
				cmds = append(cmds, autoRenewCmd(m.ctx, m.client, m.creds.Email, m.creds.Password, m.currentNetwork))
			} else {
				m.toast = "session expired — sign in again"
				m = m.initLoginInputs(false)
			}
		} else {
			m.toast = "load channels: " + msg.err.Error()
		}

	case routineReadyMsg:
		dlog("routineReadyMsg: channel=%s net=%s tracks=%d", msg.channel.Key, msg.network, len(msg.tracks))
		if len(msg.tracks) > 0 {
			rt := msg.tracks[0]
			dlog("routineReadyMsg: first track=%d artist=%q title=%q combined=%q audioURL=%q",
				rt.TrackID, rt.Artist, rt.Title, rt.Track, rt.AudioURL())
		}
		m.resolving = false
		m.currentChannel = msg.channel.Key
		m.playingNetwork = msg.network
		m.currentNetwork = msg.network
		m.cfg.LastChannel = msg.channel.Key
		m.cfg.LastNetwork = msg.network
		_ = m.cfg.Save()
		m.trackTickGen++
		q := audioaddict.NewTrackQueue()
		q.Append(msg.tracks)
		q.Next() // advance past the first track (already playing)
		m.trackQueue = q
		if len(msg.tracks) > 0 {
			m.currentTrack = msg.tracks[0].ToTrack()
			m.pushMediaTitle()
			m.trackStartTime = time.Now()
			m.trackPauseElapsed = 0
			m.voteUp = 0
			m.voteDown = 0
			if cmd := m.refreshFanart(m.currentTrack, msg.channel); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if m.creds.ID != 0 {
				cmds = append(cmds, loadVoteStateCmd(m.ctx, m.client, m.playingNetwork, m.currentTrack.ID, m.creds.ID))
			}
		}
		m.statusInfo = fmt.Sprintf("on-demand (%d tracks queued)", q.Remaining())
		cmds = append(cmds, clearStatusInfoCmd())
		if t, err := parseExpiresOn(msg.expiresOn); err == nil {
			m.sessionExpiresAt = t
			dlog("routineReadyMsg: sessionExpiresAt updated to %s", t.Format(time.RFC3339))
		}
		if cmd := scheduleSessionExpiryCmd(msg.expiresOn); cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, keepalivePingCmd(m.ctx, m.client, m.playingNetwork, msg.channel.ID))

	case streamPlayingMsg:
		m.resolving = false
		m.trackQueue = nil // live-stream mode — clear any per-track queue
		if m.creds.AudioToken == "" {
			dlog("streamPlayingMsg: live-stream fallback — audio_token empty, re-login for per-track mode")
			m.toast = "per-track mode unavailable — sign in again for skip + on-demand"
		}
		m.currentChannel = msg.channel.Key
		m.playingNetwork = msg.network
		m.currentNetwork = msg.network
		m.cfg.LastChannel = msg.channel.Key
		m.cfg.LastNetwork = msg.network
		_ = m.cfg.Save()
		m.trackTickGen++
		m.trackStartTime = time.Now()
		m.trackPauseElapsed = 0
		m.voteUp = 0
		m.voteDown = 0
		m.recentTracks = nil
		gen := m.trackTickGen
		cmds = append(cmds,
			fetchTrackCmd(m.ctx, m.client, m.playingNetwork, msg.channel.ID, gen),
			tickTrackCmd(m.ctx, m.client, m.playingNetwork, msg.channel.ID, gen),
			fetchHistoryCmd(m.ctx, m.client, m.playingNetwork, msg.channel.ID),
			keepalivePingCmd(m.ctx, m.client, m.playingNetwork, msg.channel.ID),
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
			if m.creds.Email != "" && m.creds.Password != "" {
				dlog("streamErrorMsg: listenKeyDead — attempting auto-renew")
				m.toast = "session refreshing…"
				cmds = append(cmds, autoRenewCmd(m.ctx, m.client, m.creds.Email, m.creds.Password, m.currentNetwork))
			} else {
				m.toast = "listen_key rejected — sign in again"
				m = m.initLoginInputs(false)
			}
		case msg.unauthorized:
			if m.creds.Email != "" && m.creds.Password != "" {
				dlog("streamErrorMsg: unauthorized — attempting auto-renew")
				m.toast = "session refreshing…"
				cmds = append(cmds, autoRenewCmd(m.ctx, m.client, m.creds.Email, m.creds.Password, m.currentNetwork))
			} else {
				m.toast = "session expired — sign in again"
				m = m.initLoginInputs(false)
			}
		default:
			m.toast = "play: " + msg.err.Error()
		}
		m.loading = false

	case trackUpdateMsg:
		dlog("trackUpdateMsg: channelID=%d gen=%d artist=%q title=%q track=%q artURL=%q",
			msg.channelID, msg.gen, msg.track.Artist, msg.track.Title, msg.track.Track, msg.track.ArtURL)
		if msg.gen != m.trackTickGen {
			dlog("trackUpdateMsg: DROPPED stale gen=%d (current=%d) — tick chain for old channel retired", msg.gen, m.trackTickGen)
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
			trackChanged := newTrackID != 0 && newTrackID != m.currentTrack.ID
			loadVote := trackChanged && m.creds.ID != 0
			if trackChanged {
				m.trackStartTime = time.Now()
				m.trackPauseElapsed = 0
				m.voteUp = 0
				m.voteDown = 0
				cmds = append(cmds, fetchHistoryCmd(m.ctx, m.client, m.playingNetwork, msg.channelID))
			}
			m.currentTrack = msg.track
			m.pushMediaTitle()
			ch, _ := channelByID(m.channels, msg.channelID)
			if cmd := m.refreshFanart(msg.track, ch); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if loadVote {
				cmds = append(cmds, loadVoteStateCmd(m.ctx, m.client, m.playingNetwork, newTrackID, m.creds.ID))
			}
			cmds = append(cmds, tickTrackCmd(m.ctx, m.client, m.playingNetwork, msg.channelID, msg.gen))
		}

	case progressTickMsg:
		if m.playerSt == player.StatePlaying {
			cmds = append(cmds, progressTickCmd())
		} else {
			dlog("progressTickMsg: NOT re-arming — playerSt=%s", m.playerSt)
		}

	case recentTracksMsg:
		if len(msg.tracks) > 1 {
			m.recentTracks = msg.tracks[1:]
		} else {
			m.recentTracks = nil
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
		m.voteUp = msg.voteUp
		m.voteDown = msg.voteDown
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
		dlog("loginSuccessMsg: email_set=%t listen_key_len=%d session_key_len=%d premium=%t channels_loaded=%d currentChannel=%q pendingVote=%t pendingSkip=%t",
			msg.creds.Email != "", len(msg.creds.ListenKey), len(msg.creds.SessionKey), msg.creds.Premium,
			len(m.channels), m.currentChannel, m.pendingVote != nil, m.pendingSkip != nil)
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
		cmds = append(cmds, sessionCheckCmd(m.ctx, m.client, m.currentNetwork))
		if m.pendingSkip != nil {
			ps := m.pendingSkip
			m.pendingSkip = nil
			dlog("loginSuccessMsg: replaying pendingSkip (network=%s track=%d channel=%d)",
				ps.network, ps.trackID, ps.channelID)
			m.skipInFlight = true
			trackLen := int(m.currentTrack.Duration)
			cmds = append(cmds, skipTrackCmd(m.ctx, m.client, m.player, ps.network, ps.trackID, ps.channelID, trackLen, 0, ps.channel, m.trackQueue))
		}

	case loginErrorMsg:
		dlog("loginErrorMsg: %v", msg.err)
		m.loginBusy = false
		m.loginError = msg.err.Error()

	case clearStatusInfoMsg:
		m.statusInfo = ""

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

	case skipOKMsg:
		dlog("skipOKMsg: channel=%s net=%s skipsRemaining=%d routineTrack=%t",
			msg.channel.Key, msg.network, msg.skipsRemaining, msg.routineTrack != nil)
		m.skipInFlight = false
		m.currentChannel = msg.channel.Key
		m.playingNetwork = msg.network
		m.trackTickGen++
		m.trackStartTime = time.Now()
		m.trackPauseElapsed = 0
		m.voteUp = 0
		m.voteDown = 0
		if msg.routineTrack != nil {
			m.currentTrack = msg.routineTrack.ToTrack()
			m.pushMediaTitle()
			if cmd := m.refreshFanart(m.currentTrack, msg.channel); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if m.creds.ID != 0 {
				cmds = append(cmds, loadVoteStateCmd(m.ctx, m.client, m.playingNetwork, m.currentTrack.ID, m.creds.ID))
			}
		} else {
			gen := m.trackTickGen
			cmds = append(cmds,
				fetchTrackCmd(m.ctx, m.client, m.playingNetwork, msg.channel.ID, gen),
				tickTrackCmd(m.ctx, m.client, m.playingNetwork, msg.channel.ID, gen),
			)
		}
		if msg.skipsRemaining > 0 {
			m.statusInfo = fmt.Sprintf("skipped — %d skip(s) left", msg.skipsRemaining)
		} else {
			m.statusInfo = "skipped"
		}
		cmds = append(cmds, clearStatusInfoCmd())

	case skipErrMsg:
		dlog("skipErrMsg: err=%v sessionInvalid=%t", msg.err, msg.sessionInvalid)
		m.skipInFlight = false
		switch {
		case msg.sessionInvalid:
			m.pendingSkip = &pendingSkip{
				network:   msg.network,
				trackID:   msg.trackID,
				channelID: msg.channelID,
				channel:   msg.channel,
			}
			if m.creds.Email != "" && m.creds.Password != "" {
				dlog("skipErrMsg: sessionInvalid — stashing pendingSkip and auto-renewing")
				m.toast = "session refreshing…"
				cmds = append(cmds, autoRenewCmd(m.ctx, m.client, m.creds.Email, m.creds.Password, m.currentNetwork))
			} else {
				dlog("skipErrMsg: sessionInvalid — stashing pendingSkip and popping login")
				m.toast = ""
				m = m.initLoginInputs(false)
			}
		default:
			m.toast = "skip: " + msg.err.Error()
		}

	case trackAdvancedMsg:
		dlog("trackAdvancedMsg: track=%d (%s)", msg.routineTrack.TrackID, msg.routineTrack.Track)
		m.trackTickGen++
		m.trackStartTime = time.Now()
		m.trackPauseElapsed = 0
		m.voteUp = 0
		m.voteDown = 0
		m.currentTrack = msg.routineTrack.ToTrack()
		m.pushMediaTitle()
		if cmd := m.refreshFanart(m.currentTrack, msg.channel); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.creds.ID != 0 {
			cmds = append(cmds, loadVoteStateCmd(m.ctx, m.client, m.playingNetwork, m.currentTrack.ID, m.creds.ID))
		}

	case trackAdvanceErrMsg:
		dlog("trackAdvanceErrMsg: err=%v", msg.err)
		m.trackQueue = nil
		m.toast = "auto-advance failed: " + msg.err.Error()

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

	case trackErrorMsg:
		if msg.unauthorized {
			if m.creds.Email != "" && m.creds.Password != "" {
				dlog("trackErrorMsg: unauthorized — attempting auto-renew")
				m.toast = "session refreshing…"
				cmds = append(cmds, autoRenewCmd(m.ctx, m.client, m.creds.Email, m.creds.Password, m.currentNetwork))
			} else {
				dlog("trackErrorMsg: unauthorized — popping login overlay")
				m.toast = "session expired — sign in again"
				m = m.initLoginInputs(false)
			}
		}

	case sessionCheckMsg:
		if !msg.alive {
			dlog("sessionCheckMsg: health check FAILED — err=%v isNetwork=%t sessionValid=%t expiresAt=%s",
				msg.err, isNetworkError(msg.err), sessionStillValid(m.sessionExpiresAt), m.sessionExpiresAt.Format(time.RFC3339))
			if isNetworkError(msg.err) && sessionStillValid(m.sessionExpiresAt) {
				dlog("sessionCheckMsg: network error within validity window — will retry, not logging out")
				m.toast = "network unavailable — retrying…"
			} else if m.creds.Email != "" && m.creds.Password != "" {
				dlog("sessionCheckMsg: attempting auto-renew")
				m.toast = "session refreshing…"
				cmds = append(cmds, autoRenewCmd(m.ctx, m.client, m.creds.Email, m.creds.Password, m.currentNetwork))
			} else {
				m.toast = "session expired — sign in again"
				m = m.initLoginInputs(false)
			}
		}
		cmds = append(cmds, sessionCheckCmd(m.ctx, m.client, m.currentNetwork))

	case sessionExpiryMsg:
		dlog("sessionExpiryMsg: audio_token approaching expiry")
		if sessionStillValid(m.sessionExpiresAt) {
			dlog("sessionExpiryMsg: stale timer — session still valid until %s, ignoring", m.sessionExpiresAt.Format(time.RFC3339))
		} else if m.creds.Email != "" && m.creds.Password != "" {
			dlog("sessionExpiryMsg: attempting auto-renew")
			m.toast = "session refreshing…"
			cmds = append(cmds, autoRenewCmd(m.ctx, m.client, m.creds.Email, m.creds.Password, m.currentNetwork))
		} else {
			m.toast = "session expiring soon — sign in again for uninterrupted playback"
			m = m.initLoginInputs(false)
		}

	case autoRenewMsg:
		if msg.err != nil {
			dlog("autoRenewMsg: auto-renew FAILED err=%v isNetwork=%t sessionValid=%t expiresAt=%s",
				msg.err, isNetworkError(msg.err), sessionStillValid(m.sessionExpiresAt), m.sessionExpiresAt.Format(time.RFC3339))
			if isNetworkError(msg.err) && sessionStillValid(m.sessionExpiresAt) {
				dlog("autoRenewMsg: network error within validity window — keeping session, will retry on next check")
				m.toast = "network unavailable — session still valid, retrying…"
			} else {
				m.toast = "session expired — sign in again"
				m = m.initLoginInputs(false)
			}
		} else {
			dlog("autoRenewMsg: auto-renew OK — session refreshed silently")
			m.creds = msg.creds
			m.toast = ""
			if m.pendingVote != nil {
				pv := m.pendingVote
				m.pendingVote = nil
				m.voteInFlight = true
				cmds = append(cmds, voteCmd(m.ctx, m.client, pv.network, pv.trackID, pv.channelID, pv.dir))
			}
			if m.pendingSkip != nil {
				ps := m.pendingSkip
				m.pendingSkip = nil
				m.skipInFlight = true
				trackLen := int(m.currentTrack.Duration)
				cmds = append(cmds, skipTrackCmd(m.ctx, m.client, m.player, ps.network, ps.trackID, ps.channelID, trackLen, 0, ps.channel, m.trackQueue))
			}
		}

	case keepalivePingMsg:
		if msg.err != nil {
			dlog("keepalivePingMsg: ping FAILED err=%v", msg.err)
		}
		if m.currentChannel != "" {
			chID := m.channelIDForKey(m.currentChannel)
			if chID != 0 {
				cmds = append(cmds, keepalivePingCmd(m.ctx, m.client, m.playingNetwork, chID))
			}
		}

	case voteErrMsg:
		m.voteInFlight = false
		switch {
		case msg.sessionInvalid:
			m.pendingVote = &pendingVote{
				network:   msg.network,
				trackID:   msg.trackID,
				channelID: msg.channelID,
				dir:       msg.dir,
			}
			if m.creds.Email != "" && m.creds.Password != "" {
				dlog("voteErrMsg: sessionInvalid — stashing pendingVote and auto-renewing")
				m.toast = "session refreshing…"
				cmds = append(cmds, autoRenewCmd(m.ctx, m.client, m.creds.Email, m.creds.Password, m.currentNetwork))
			} else {
				dlog("voteErrMsg: sessionInvalid — stashing pendingVote and popping login")
				m.toast = ""
				m = m.initLoginInputs(false)
			}
		case msg.unauthorized:
			if m.creds.Email != "" && m.creds.Password != "" {
				dlog("voteErrMsg: unauthorized — attempting auto-renew")
				m.toast = "session refreshing…"
				cmds = append(cmds, autoRenewCmd(m.ctx, m.client, m.creds.Email, m.creds.Password, m.currentNetwork))
			} else {
				m.toast = "session expired — sign in again"
				m = m.initLoginInputs(false)
			}
		default:
			m.toast = "vote: " + msg.err.Error()
		}
	}

	return m, tea.Batch(cmds...)
}

// pushMediaTitle sends structured artist/title to the mpv Lua script so
// macOS Now Playing (and MPRIS on Linux) shows clean API-sourced metadata
// instead of the raw ICY stream title which often has encoding artifacts.
func (m *Model) pushMediaTitle() {
	if m.player == nil {
		return
	}
	t := m.currentTrack
	artist, title := t.Artist, t.Title
	if artist == "" && title == "" {
		if t.Track == "" {
			return
		}
		title = t.Track
	}
	dlog("pushMediaTitle: artist=%q title=%q (raw track=%q)", artist, title, t.Track)
	_ = m.player.SetTrackMetadata(artist, title)
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
	case FocusAbout:
		return m.viewAbout()
	default:
		return m.viewHome()
	}
}
