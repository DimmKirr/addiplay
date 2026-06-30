package ui

import "github.com/charmbracelet/bubbles/key"

type keymap struct {
	Quit        key.Binding
	Up          key.Binding
	Down        key.Binding
	Play        key.Binding
	PauseResume key.Binding
	VolumeUp    key.Binding
	VolumeDown  key.Binding
	Favorite    key.Binding
	Search      key.Binding
	Network     key.Binding
	SwitchTab   key.Binding
	// Logout clears stored creds and re-opens the login overlay so the
	// user can switch accounts (or reset a wedged session) without
	// leaving the TUI. Capital L to avoid collision with the volume
	// keys' '-' (which `l` would be a lower-priority lookalike for).
	Logout key.Binding
	// Like toggles the AudioAddict vote on the currently-playing track.
	// Lowercase `l` was previously unbound; uppercase `L` stays as logout.
	Like key.Binding
	// Dislike is the negative-signal counterpart to Like (DIMM-382).
	// Picked `s` over `d` to leave room for a future "dump channel" or
	// "downvote+skip-channel" action — and because AudioAddict has no
	// true skip-track endpoint, so reserving `s` for that would mislead.
	Dislike key.Binding
	// Help opens the keymap overlay (DIMM-392). The header has long
	// advertised "[?] keys"; this binding makes it real.
	Help key.Binding
}

var keys = keymap{
	Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	Up:          key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Play:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "play")),
	PauseResume: key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "pause/resume")),
	VolumeUp:    key.NewBinding(key.WithKeys("+", "="), key.WithHelp("+", "vol up")),
	VolumeDown:  key.NewBinding(key.WithKeys("-", "_"), key.WithHelp("-", "vol down")),
	Favorite:    key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "favorite")),
	Search:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
	Network:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "network")),
	SwitchTab:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "favs/all")),
	Logout:      key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "logout")),
	Like:        key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "like")),
	Dislike:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "dislike")),
	Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "show keys")),
}
