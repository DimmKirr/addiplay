package cmd

import (
	"fmt"
	"io"

	"github.com/dimmkirr/addiplay/internal/cache"
)

// runClearCache backs `addiplay --clear-cache`. Removes the cache root
// (channel JSON, CDN thumbnails, track metadata) and prints a one-line
// confirmation. Safe to call on a never-used install — Clear() is
// idempotent.
//
// `cacheDir` is taken as a parameter so tests can target a temp dir
// without bleeding into the real user cache.
func runClearCache(o io.Writer, cacheDir string) error {
	store, err := cache.NewFS(cacheDir)
	if err != nil {
		return err
	}
	if err := store.Clear(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o, "cleared cache: %s\n", cacheDir)
	return nil
}
