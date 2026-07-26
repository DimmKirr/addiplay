package nowplaying

import "io"

// Option configures a Provider at construction time.
type Option func(*config)

type config struct {
	logWriter io.Writer
}

func options(opts []Option) config {
	var c config
	for _, o := range opts {
		o(&c)
	}
	return c
}

// WithLogWriter directs diagnostic output to w. On platforms with a
// native provider this is used for debug logging; on the stub/log
// provider it receives all call traces.
func WithLogWriter(w io.Writer) Option {
	return func(c *config) { c.logWriter = w }
}
