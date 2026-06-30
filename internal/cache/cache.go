// Package cache is a small disk-backed (and in-memory) key-value store
// used by addiplay to persist things between runs that are expensive to
// re-fetch — channel-list JSON, CDN thumbnails, static track metadata.
//
// The store is intentionally tiny: buckets are subdirectories, keys are
// hashed to filenames, values are opaque byte blobs. Serialization
// (JSON, PNG, …) is the caller's responsibility.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is one cached blob plus its persistence timestamp. FetchedAt is
// taken from the file's mtime so age checks (sweep, stale-while-revalidate)
// work across process restarts.
type Entry struct {
	Body      []byte
	FetchedAt time.Time
}

// Store is the abstract interface every cache backend implements.
type Store interface {
	Get(bucket, key string) (Entry, bool, error)
	Put(bucket, key string, body []byte) error
}

// Defaults applied by NewFS when the corresponding field is zero. Tests
// override them by setting the FS fields directly after construction.
const (
	defaultMaxAge   = 30 * 24 * time.Hour
	defaultMaxBytes = 50 << 20 // 50 MiB
)

// FS is the filesystem-backed Store. Root is the directory under which
// each bucket becomes a subdirectory.
type FS struct {
	Root string

	// MaxAge: entries older than this are evicted on Sweep().
	// Zero means "use defaultMaxAge". Set to a negative duration
	// (e.g. -1) to disable age-based eviction in tests.
	MaxAge time.Duration

	// MaxBytes: total file-content bytes across all buckets. When
	// exceeded after the age pass, Sweep evicts oldest-mtime-first
	// until under cap. Zero = defaultMaxBytes; negative = disabled.
	MaxBytes int64
}

// DefaultDir resolves the cache root for this user:
//
//  1. $ADDIPLAY_CACHE_DIR — explicit override, used by tests and by
//     users who want their cache somewhere unusual.
//  2. os.UserCacheDir() + "/addiplay" — the per-OS default
//     (macOS: ~/Library/Caches/addiplay, Linux: ~/.cache/addiplay
//     honoring $XDG_CACHE_HOME, Windows: %LocalAppData%\addiplay).
//
// Mirrors the resolution used by internal/config so the two are easy
// to reason about together.
func DefaultDir() (string, error) {
	if override := os.Getenv("ADDIPLAY_CACHE_DIR"); override != "" {
		return override, nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "addiplay"), nil
}

// NewFS returns a disk-backed Store rooted at rootDir. The directory is
// created on first Put (lazy) so callers don't pay for it until the
// cache is actually exercised.
func NewFS(rootDir string) (*FS, error) {
	if rootDir == "" {
		return nil, errors.New("cache: NewFS requires non-empty rootDir")
	}
	return &FS{Root: rootDir}, nil
}

// Mem is an in-memory Store used by tests that don't want to touch
// disk. Goroutine-safe.
type Mem struct {
	mu      sync.Mutex
	entries map[string]Entry // key = bucket + "\x00" + key
}

// NewMem returns an empty in-memory Store.
func NewMem() *Mem {
	return &Mem{entries: map[string]Entry{}}
}

// Put stores body under (bucket, key) with FetchedAt = now.
func (m *Mem) Put(bucket, key string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copyBody := make([]byte, len(body))
	copy(copyBody, body)
	m.entries[bucket+"\x00"+key] = Entry{Body: copyBody, FetchedAt: time.Now()}
	return nil
}

// Get returns the stored Entry, or (_, false, nil) if absent.
func (m *Mem) Get(bucket, key string) (Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[bucket+"\x00"+key]
	if !ok {
		return Entry{}, false, nil
	}
	return e, true, nil
}

// HashKey collapses a user-supplied identifier (URL, network slug, etc.)
// into a fixed-length hex filename. The cache treats keys as opaque, so
// hashing keeps filesystem-unsafe characters (?, /, &, …) out of paths.
// Exported because callers occasionally want to locate the on-disk file
// for a given key (debugging, --doctor, integration tests).
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// path returns the on-disk file for (bucket, key).
func (s *FS) path(bucket, key string) string {
	return filepath.Join(s.Root, bucket, HashKey(key))
}

// Put writes body under (bucket, key). Atomic via .tmp + rename.
func (s *FS) Put(bucket, key string, body []byte) error {
	dir := filepath.Join(s.Root, bucket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "put-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(bucket, key))
}

// Sweep walks every bucket and evicts entries by two rules:
//
//  1. Age: anything with mtime older than MaxAge is removed.
//  2. Size cap: if remaining total bytes exceed MaxBytes, oldest-mtime-
//     first entries are removed until under cap.
//
// Sweep is safe to call concurrently with Put/Get on different keys; on
// the rare race against a Put that's mid-rename it may briefly miscount,
// but the cache still converges on the next sweep. Errors on individual
// files are recorded and the first is returned, but the walk continues —
// one bad file shouldn't strand the rest of the cache.
func (s *FS) Sweep() error {
	maxAge := s.MaxAge
	if maxAge == 0 {
		maxAge = defaultMaxAge
	}

	type fileInfo struct {
		path  string
		size  int64
		mtime time.Time
	}
	var firstErr error
	var live []fileInfo
	now := time.Now()

	walkErr := filepath.WalkDir(s.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // root not created yet — fine
			}
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		// Skip .tmp files left behind by a crashed Put — they're not
		// committed cache entries.
		name := d.Name()
		if len(name) > 4 && name[len(name)-4:] == ".tmp" {
			return nil
		}
		if maxAge > 0 && now.Sub(info.ModTime()) > maxAge {
			if rmErr := os.Remove(path); rmErr != nil && firstErr == nil {
				firstErr = rmErr
			}
			return nil
		}
		live = append(live, fileInfo{path: path, size: info.Size(), mtime: info.ModTime()})
		return nil
	})
	if walkErr != nil && firstErr == nil {
		firstErr = walkErr
	}

	// Size-cap pass.
	maxBytes := s.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxBytes
	}
	if maxBytes <= 0 {
		return firstErr
	}
	var total int64
	for _, f := range live {
		total += f.size
	}
	if total <= maxBytes {
		return firstErr
	}
	// Sort oldest-first and evict until we're under the cap.
	sort.Slice(live, func(i, j int) bool {
		return live[i].mtime.Before(live[j].mtime)
	})
	for _, f := range live {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		total -= f.size
	}
	return firstErr
}

// Stats describes the on-disk footprint of a cache. Used by --doctor
// to surface a one-screen view of the cache's health.
type Stats struct {
	Root       string         // resolved cache root
	TotalBytes int64          // sum of all file sizes
	Buckets    map[string]int // bucket name → entry count
}

// Stat walks the cache root and returns a current snapshot. A missing
// root is not an error — returns a zero-value Stats so callers don't
// have to special-case fresh installs.
func (s *FS) Stat() (Stats, error) {
	out := Stats{Root: s.Root, Buckets: map[string]int{}}
	walkErr := filepath.WalkDir(s.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() || path == s.Root {
			return nil
		}
		// .tmp files from crashed Puts don't count as cache entries.
		name := d.Name()
		if len(name) > 4 && name[len(name)-4:] == ".tmp" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// bucket = the immediate subdir of root that contains this file
		rel, _ := filepath.Rel(s.Root, path)
		bucket := filepath.Dir(rel)
		out.Buckets[bucket]++
		out.TotalBytes += info.Size()
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return out, walkErr
	}
	return out, nil
}

// Clear removes the entire cache root. Idempotent — a missing dir is
// not an error, so `addiplay --clear-cache` works on a never-used
// install.
func (s *FS) Clear() error {
	err := os.RemoveAll(s.Root)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Get reads (bucket, key). Returns (_, false, nil) when the entry does
// not exist — a missing file is "not cached", not an error.
func (s *FS) Get(bucket, key string) (Entry, bool, error) {
	path := s.path(bucket, key)
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Entry{}, false, nil
		}
		return Entry{}, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Entry{}, false, err
	}
	return Entry{Body: body, FetchedAt: info.ModTime()}, true, nil
}
