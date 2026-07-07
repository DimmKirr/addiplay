package cache

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestStore_PutThenGet_roundTrips is the smoke test for the disk-backed
// store: write bytes under (bucket, key), read them back identical.
func TestStore_PutThenGet_roundTrips(t *testing.T) {
	store, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	want := []byte("hello world")
	if err := store.Put("thumbs", "abc", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entry, ok, err := store.Get("thumbs", "abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok=false, want true")
	}
	if string(entry.Body) != string(want) {
		t.Errorf("body = %q, want %q", entry.Body, want)
	}
	if entry.FetchedAt.IsZero() {
		t.Error("FetchedAt is zero; want mtime of the cache file")
	}
}

// TestStore_Get_missingKey_returnsFalse confirms a missing entry is the
// "not cached" signal, not an error — callers don't have to disambiguate
// fs.ErrNotExist from real I/O failures.
func TestStore_Get_missingKey_returnsFalse(t *testing.T) {
	store, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	entry, ok, err := store.Get("thumbs", "does-not-exist")
	if err != nil {
		t.Fatalf("Get returned err for missing key: %v", err)
	}
	if ok {
		t.Error("Get: ok=true for missing key, want false")
	}
	if entry.Body != nil {
		t.Errorf("Get: body=%q for missing key, want nil", entry.Body)
	}
}

// TestStore_Put_atomicOnConcurrentWrites blasts the same key with N
// goroutines writing distinct payloads. After all writers finish, Get
// must return a payload that was written by SOME writer in full —
// never a half-written file, never a mix of two payloads. This
// exercises the .tmp + rename atomic-write path.
func TestStore_Put_atomicOnConcurrentWrites(t *testing.T) {
	store, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	const writers = 16
	// Each writer's payload is its index repeated; make the payload
	// large enough that a non-atomic write would tear visibly.
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte(fmt.Sprintf("w%02d|", i)), 4096)
	}

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			if err := store.Put("thumbs", "contended", payloads[i]); err != nil {
				t.Errorf("Put #%d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	entry, ok, err := store.Get("thumbs", "contended")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok=false after writers ran, want true")
	}
	// The final body must match one of the writers' payloads exactly.
	matched := false
	for _, p := range payloads {
		if bytes.Equal(entry.Body, p) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("Get body did not match any writer's payload (len=%d) — torn write", len(entry.Body))
	}
}

// setMtime back-dates the cache file so age-based sweep tests don't have
// to sleep. Helper kept private to tests.
func setMtime(t *testing.T, path string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}

// TestStore_Sweep_evictsOldEntries — an entry whose mtime is older than
// MaxAge must be deleted by Sweep().
func TestStore_Sweep_evictsOldEntries(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFS(dir)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	store.MaxAge = 30 * 24 * time.Hour

	if err := store.Put("thumbs", "old", []byte("ancient")); err != nil {
		t.Fatalf("Put old: %v", err)
	}
	if err := store.Put("thumbs", "fresh", []byte("recent")); err != nil {
		t.Fatalf("Put fresh: %v", err)
	}
	// Back-date the "old" entry to 31 days ago.
	setMtime(t, filepath.Join(dir, "thumbs", HashKey("old")), 31*24*time.Hour)

	if err := store.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if _, ok, _ := store.Get("thumbs", "old"); ok {
		t.Error("old entry survived Sweep, want evicted")
	}
	if _, ok, _ := store.Get("thumbs", "fresh"); !ok {
		t.Error("fresh entry was evicted, want kept")
	}
}

// TestStore_Sweep_respectsSizeCap_evictsOldestFirst — fills the cache
// past MaxBytes with entries of varying mtimes, sweeps, and asserts:
//   - total disk usage drops to ≤ MaxBytes
//   - the OLDEST entries are gone; the newest survive
func TestStore_Sweep_respectsSizeCap_evictsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFS(dir)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	// Disable age eviction so this test isolates the size-cap path.
	store.MaxAge = -1
	store.MaxBytes = 3500 // each entry is 1 KiB; 5 entries = 5120 → evict 2 → 3072 ≤ 3500

	payload := bytes.Repeat([]byte{'x'}, 1024)
	// Write 5 entries, back-date their mtimes monotonically: e0 oldest,
	// e4 newest. Total = 5120 B; cap = 3500 → expect e0 and e1 evicted.
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("e%d", i)
		if err := store.Put("thumbs", key, payload); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
		age := time.Duration(5-i) * time.Hour
		setMtime(t, filepath.Join(dir, "thumbs", HashKey(key)), age)
	}

	if err := store.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// Two oldest gone, three newest still here.
	for _, gone := range []string{"e0", "e1"} {
		if _, ok, _ := store.Get("thumbs", gone); ok {
			t.Errorf("%s survived size-cap eviction, want gone", gone)
		}
	}
	for _, kept := range []string{"e2", "e3", "e4"} {
		if _, ok, _ := store.Get("thumbs", kept); !ok {
			t.Errorf("%s evicted, want kept (it's newer)", kept)
		}
	}

	// Walk the dir and confirm total bytes is under the cap.
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, _ := d.Info()
		total += info.Size()
		return nil
	})
	if total > store.MaxBytes {
		t.Errorf("after sweep, total=%d > cap=%d", total, store.MaxBytes)
	}
}

// TestStore_Sweep_walksAllBuckets — entries in different buckets get
// age-evicted in a single Sweep() pass. Regression guard against an
// implementation that only walks one hardcoded bucket.
func TestStore_Sweep_walksAllBuckets(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFS(dir)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	store.MaxAge = 30 * 24 * time.Hour

	buckets := []string{"thumbs", "channels", "tracks"}
	for _, b := range buckets {
		if err := store.Put(b, "stale", []byte("old")); err != nil {
			t.Fatalf("Put %s: %v", b, err)
		}
		setMtime(t, filepath.Join(dir, b, HashKey("stale")), 31*24*time.Hour)
	}

	if err := store.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	for _, b := range buckets {
		if _, ok, _ := store.Get(b, "stale"); ok {
			t.Errorf("bucket %q entry survived sweep, want evicted", b)
		}
	}
}

// TestStore_Clear_removesEverything — Clear() wipes the cache root so
// `addiplay --clear-cache` is a no-foot-gun reset.
func TestStore_Clear_removesEverything(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFS(dir)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	for _, b := range []string{"thumbs", "channels"} {
		if err := store.Put(b, "k", []byte("v")); err != nil {
			t.Fatalf("Put %s: %v", b, err)
		}
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	for _, b := range []string{"thumbs", "channels"} {
		if _, ok, _ := store.Get(b, "k"); ok {
			t.Errorf("bucket %q entry survived Clear, want gone", b)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cache root still exists after Clear: stat err=%v", err)
	}
}

// TestStore_Clear_idempotentOnMissingDir — Clear() on a never-created
// dir must not return an error. Lets the --clear-cache flag work even
// when the user has never run the TUI.
func TestStore_Clear_idempotentOnMissingDir(t *testing.T) {
	store, err := NewFS(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Errorf("Clear on missing dir: %v, want nil", err)
	}
}

// TestDefaultDir_respectsADDIPLAY_CACHE_DIR — explicit env override
// wins over the OS default. Used by tests/CI to redirect the cache
// without touching the user's real ~/Library/Caches.
func TestDefaultDir_respectsADDIPLAY_CACHE_DIR(t *testing.T) {
	t.Setenv("ADDIPLAY_CACHE_DIR", "/tmp/explicit-addiplay-cache")
	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if got != "/tmp/explicit-addiplay-cache" {
		t.Errorf("DefaultDir = %q, want %q", got, "/tmp/explicit-addiplay-cache")
	}
}

// TestDefaultDir_respectsXDGCacheHome — on Linux, $XDG_CACHE_HOME is
// honored via os.UserCacheDir, with /addiplay appended.
func TestDefaultDir_respectsXDGCacheHome(t *testing.T) {
	t.Setenv("ADDIPLAY_CACHE_DIR", "") // make sure override isn't masking
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	// os.UserCacheDir on macOS ignores $XDG_CACHE_HOME (it returns
	// ~/Library/Caches), so the path check has to be OS-aware. The
	// invariant that's portable: DefaultDir must end with /addiplay.
	want := "/addiplay"
	if !bytes.HasSuffix([]byte(got), []byte(want)) {
		t.Errorf("DefaultDir = %q, want suffix %q", got, want)
	}
}

// TestStat_reportsPerBucketCountsAndTotalSize — feeds --doctor a
// reliable cache picture so users can spot a runaway thumbnail dir.
func TestStat_reportsPerBucketCountsAndTotalSize(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFS(dir)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	// Seed: 2 entries in thumbs (10 + 20 bytes), 1 in channels (5 bytes).
	if err := store.Put("thumbs", "a", bytes.Repeat([]byte{'x'}, 10)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Put("thumbs", "b", bytes.Repeat([]byte{'x'}, 20)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Put("channels", "c", bytes.Repeat([]byte{'x'}, 5)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stat, err := store.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.TotalBytes != 35 {
		t.Errorf("TotalBytes = %d, want 35", stat.TotalBytes)
	}
	if stat.Buckets["thumbs"] != 2 {
		t.Errorf("Buckets[thumbs] = %d, want 2", stat.Buckets["thumbs"])
	}
	if stat.Buckets["channels"] != 1 {
		t.Errorf("Buckets[channels] = %d, want 1", stat.Buckets["channels"])
	}
}

// TestStat_missingDir_returnsEmpty — doctor must work on a fresh
// install with no cache yet.
func TestStat_missingDir_returnsEmpty(t *testing.T) {
	store, err := NewFS(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	stat, err := store.Stat()
	if err != nil {
		t.Errorf("Stat on missing dir: %v, want nil", err)
	}
	if stat.TotalBytes != 0 || len(stat.Buckets) != 0 {
		t.Errorf("Stat on missing dir = %+v, want zero-value", stat)
	}
}

// TestMemStore_satisfiesContract confirms NewMem() honours the basic
// Put/Get round-trip + missing-key contract; lets callers swap a
// real-disk Store for an in-memory one in unit tests.
func TestMemStore_satisfiesContract(t *testing.T) {
	store := NewMem()
	if _, ok, _ := store.Get("thumbs", "missing"); ok {
		t.Error("Get on empty mem store: ok=true, want false")
	}
	if err := store.Put("thumbs", "abc", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entry, ok, err := store.Get("thumbs", "abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok=false after Put")
	}
	if string(entry.Body) != "hello" {
		t.Errorf("body=%q, want %q", entry.Body, "hello")
	}
	if entry.FetchedAt.IsZero() {
		t.Error("FetchedAt is zero in mem store")
	}
}
