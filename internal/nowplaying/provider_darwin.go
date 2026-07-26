//go:build darwin

package nowplaying

// New returns the macOS provider backed by MPNowPlayingInfoCenter and
// MPRemoteCommandCenter (CGo + ObjC). Media-key commands are delivered
// to handler.
func New(handler Handler, opts ...Option) Provider {
	return newDarwin(handler, opts...)
}
