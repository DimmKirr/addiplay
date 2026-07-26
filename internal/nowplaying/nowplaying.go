// Package nowplaying abstracts OS-level "now playing" integration across
// platforms: macOS MPNowPlayingInfoCenter, Linux MPRIS (D-Bus), and a
// logging fallback for unsupported platforms and CI.
//
// Each platform provides a Provider via New(). The caller sets track
// metadata and playback state; the Provider pushes them to the OS and
// routes incoming media-key commands (play/pause, next, previous) back
// through a callback.
package nowplaying

// Command represents a media-key action received from the OS.
type Command int

const (
	CmdPlayPause Command = iota
	CmdNext
	CmdPrevious
)

func (c Command) String() string {
	return [...]string{"PlayPause", "Next", "Previous"}[c]
}

// Handler is called on the goroutine that processes OS events when a
// media key is pressed (keyboard, AirPods, Control Center, MPRIS client).
type Handler func(Command)

// Provider is the platform-specific now-playing backend.
type Provider interface {
	// Update sets the currently-playing track metadata. Calling Update
	// implicitly marks playback state as playing. durationSec is the
	// track length in seconds (0 if unknown, e.g. live radio).
	// artURL is the fully-resolved image URL for album art (empty if
	// unavailable); on macOS the provider downloads and pushes it to
	// Control Center asynchronously.
	Update(artist, title string, durationSec int, artURL string)

	// SetPlaying reports whether audio is actively playing (true) or
	// paused (false). The OS uses this to show the correct icon in
	// Control Center / MPRIS / etc.
	SetPlaying(playing bool)

	// Close tears down the OS registration and releases resources.
	// Safe to call more than once.
	Close()
}
