package cmd

import (
	"fmt"
	"io"

	"github.com/dimmkirr/addiplay/internal/cache"
)

// sweepCacheBestEffort runs a cache sweep at startup to enforce the
// 30d-age and 50 MiB-size limits without making the user wait or fail
// to launch when something goes wrong on a corrupt cache. Any error is
// logged to `log` and swallowed — boot continues regardless. `log` may
// be nil, in which case diagnostics are simply dropped.
//
// Called from runTUI and runHeadlessPlay before model construction so a
// stale or oversized cache doesn't burn disk indefinitely.
func sweepCacheBestEffort(log io.Writer) {
	dir, err := cache.DefaultDir()
	if err != nil {
		writeLog(log, "[cache] resolve default dir failed: %v\n", err)
		return
	}
	store, err := cache.NewFS(dir)
	if err != nil {
		writeLog(log, "[cache] NewFS(%s) failed: %v\n", dir, err)
		return
	}
	if err := store.Sweep(); err != nil {
		writeLog(log, "[cache] sweep failed (continuing): %v\n", err)
	}
}

func writeLog(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprintf(w, format, args...)
}
