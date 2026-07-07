package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunClearCache_wipesDirAndConfirms verifies the --clear-cache
// action handler removes the cache root and prints a confirmation
// mentioning the path it cleared, so the user knows it ran.
func TestRunClearCache_wipesDirAndConfirms(t *testing.T) {
	root := t.TempDir()
	cacheDir := filepath.Join(root, "addiplay")
	// Seed something that should be wiped.
	if err := os.MkdirAll(filepath.Join(cacheDir, "thumbs"), 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "thumbs", "abc"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	var out bytes.Buffer
	if err := runClearCache(&out, cacheDir); err != nil {
		t.Fatalf("runClearCache: %v", err)
	}

	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Errorf("cache dir still exists after clear: stat err=%v", err)
	}
	if !strings.Contains(out.String(), cacheDir) {
		t.Errorf("output %q does not mention the cleared path %q", out.String(), cacheDir)
	}
}

// TestRunClearCache_idempotent — clearing a never-created cache works
// without error so a fresh install doesn't surface a confusing failure.
func TestRunClearCache_idempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-existed")
	var out bytes.Buffer
	if err := runClearCache(&out, dir); err != nil {
		t.Errorf("runClearCache on missing dir: %v", err)
	}
}
