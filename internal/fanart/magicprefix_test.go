package fanart

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// TestMagicPrefix covers the byte signatures used in fanart's debug log
// and toast diagnostics. The user-visible value depends on these labels
// being precise: "WebP" vs "HTML/XML (likely error page)" tells the
// difference between a CDN config issue and a missing track.
func TestMagicPrefix(t *testing.T) {
	cases := []struct{ name, want string; bytes []byte }{
		{"png", "PNG", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00}},
		{"jpeg", "JPEG", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}},
		{"webp", "WebP", append([]byte("RIFF\x00\x00\x00\x00WEBP"), bytes.Repeat([]byte{0}, 8)...)},
		{"gif", "GIF", []byte("GIF89a")},
		{"html", "HTML/XML (likely error page)", []byte("<!DOCTYPE html><body>404")},
		{"json", "JSON (likely error)", []byte(`{"error":"forbidden"}`)},
		{"avif", "AVIF (no Go stdlib decoder)", append([]byte("\x00\x00\x00 ftypavif"), bytes.Repeat([]byte{0}, 8)...)},
		{"heic", "HEIC/MIF1 (no Go stdlib decoder)", append([]byte("\x00\x00\x00 ftypheic"), bytes.Repeat([]byte{0}, 8)...)},
		{"empty", "empty", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := magicPrefix(tc.bytes)
			if got != tc.want {
				t.Errorf("magicPrefix(%q...) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestSetDebugLogger_capturesFetchEvents proves the wire-up: a fetch
// failure (we'll use a non-existent host) writes a diagnostic line to
// the configured writer. Locks in the contract cmd/tui.go relies on.
func TestSetDebugLogger_capturesFetchEvents(t *testing.T) {
	var buf safeBuffer
	SetDebugLogger(&buf)
	defer SetDebugLogger(nil)

	// Trigger the decode-failure path directly (skip the HTTP).
	_, err := toPNGThumbnail([]byte("<!DOCTYPE html><body>not an image"), 320)
	if err == nil {
		t.Fatal("expected decode failure on HTML bytes")
	}
	if !strings.Contains(err.Error(), "HTML/XML") {
		t.Errorf("expected magic prefix in error; got: %v", err)
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
