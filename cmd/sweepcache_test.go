package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dimmkirr/addiplay/internal/cache"
)

// TestSweepCacheBestEffort_evictsOldEntries — the boot-time sweep helper
// actually runs Sweep against the configured cache dir. Eviction must
// happen on the happy path; sweep errors must never propagate.
func TestSweepCacheBestEffort_evictsOldEntries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ADDIPLAY_CACHE_DIR", root)

	store, err := cache.NewFS(root)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	store.MaxAge = 30 * 24 * time.Hour

	if err := store.Put("thumbs", "ancient", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	old := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "thumbs", cache.HashKey("ancient")), old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	var log bytes.Buffer
	sweepCacheBestEffort(&log)

	if _, ok, _ := store.Get("thumbs", "ancient"); ok {
		t.Error("entry survived sweepCacheBestEffort, want evicted")
	}
}

// TestSweepCacheBestEffort_swallowsErrors — sweep against an
// unreachable cache dir must not panic or surface an error; boot
// continues.
func TestSweepCacheBestEffort_swallowsErrors(t *testing.T) {
	// Point at a path whose parent isn't writable, so any Sweep
	// attempt would fail. The helper must still return cleanly.
	t.Setenv("ADDIPLAY_CACHE_DIR", "/proc/cannot-be-a-dir/addiplay")
	var log bytes.Buffer
	sweepCacheBestEffort(&log)
	// Reaching this line without panic = pass.
}
