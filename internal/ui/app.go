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
	Channels(ctx context.Context, network string) ([]audioaddict.Channel, error)
	StreamURL(ctx context.Context, network, channel, listenKey string, q audioaddict.Quality) (string, error)
	CurrentlyPlaying(ctx context.Context, network string, channelID int64) (audioaddict.Track, error)
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
	creds     creds.Creds
	client    AudioClient
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
	searchInput textinput.Model
	netCursor   int

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
}

// NewModel constructs the root model with explicit client and player
// constructor. cmd/tui.go injects the real ones; cmd/demo.go injects fakes.
func NewModel(ctx context.Context, c creds.Creds, client AudioClient, newPlayer NewPlayerFunc) Model {
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
		switch m.focus {
		case FocusSearch:
			return m.updateSearch(k)
		case FocusNetworkPicker:
			return m.updateNetworkPicker(k)
		case FocusLogin:
			return m.updateLogin(k)
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
		if m.cfg.LastChannel != "" && m.player != nil {
			for i, ch := range m.channels {
				if ch.Key == m.cfg.LastChannel {
					m.selIdx = i
					cmds = append(cmds, playSelectedCmd(m.ctx, m.client, m.player, m.currentNetwork, ch, m.creds.ListenKey))
					m.currentChannel = ch.Key
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
		if msg.unauthorized {
			m.toast = "session expired — sign in again"
			m = m.initLoginInputs(false)
		} else {
			m.toast = "play: " + msg.err.Error()
		}
		m.loading = false

	case trackUpdateMsg:
		if msg.gen != m.trackTickGen {
			break
		}
		if msg.channelID == channelIDFromKey(m.channels, m.currentChannel) || m.playingNetwork != m.currentNetwork {
			m.currentTrack = msg.track
			ch, _ := channelByID(m.channels, msg.channelID)
			if cmd := m.refreshFanart(msg.track, ch); cmd != nil {
				cmds = append(cmds, cmd)
			}
			cmds = append(cmds, tickTrackCmd(m.ctx, m.client, m.playingNetwork, msg.channelID, msg.gen))
		}

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
		m.creds = msg.creds
		m.focus = FocusChannels
		m.loginBusy = false
		m.loginError = ""
		// Re-load channels with the fresh listen_key.
		cmds = append(cmds, loadChannelsCmd(m.ctx, m.client, m.currentNetwork))

	case loginErrorMsg:
		m.loginBusy = false
		m.loginError = msg.err.Error()
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
	default:
		return m.viewHome()
	}
}
