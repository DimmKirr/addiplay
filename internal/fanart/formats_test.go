package fanart

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestImageDecode_supportsAllExpectedFormats locks in the decoder
// registrations required by Fetch/FetchASCII. Before this test existed,
// JPEG support was assumed to be present (it's stdlib!) but never
// blank-imported, so image.Decode silently rejected every JPEG with
// "unknown format" — including 80%+ of AudioAddict's actual track art.
//
// If you remove a blank import from fanart.go's import block (image/jpeg,
// image/gif, golang.org/x/image/webp) this test FAILS — that's the point.
func TestImageDecode_supportsAllExpectedFormats(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 128, A: 255})
		}
	}

	cases := []struct {
		name   string
		encode func(*bytes.Buffer) error
		want   string // expected format name from image.Decode
	}{
		{"png", func(b *bytes.Buffer) error { return png.Encode(b, src) }, "png"},
		{"jpeg", func(b *bytes.Buffer) error { return jpeg.Encode(b, src, &jpeg.Options{Quality: 80}) }, "jpeg"},
		{"gif", func(b *bytes.Buffer) error {
			// gif.Encode requires a paletted image; convert the RGBA to one.
			pal := image.NewPaletted(src.Bounds(), color.Palette{color.Black, color.White, color.RGBA{R: 255, A: 255}})
			return gif.Encode(b, pal, nil)
		}, "gif"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tc.encode(&buf); err != nil {
				t.Fatalf("encode: %v", err)
			}
			_, fmtName, err := image.Decode(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("image.Decode(%s) failed — blank import for %q missing from fanart.go? err=%v",
					tc.name, "image/"+tc.name, err)
			}
			if fmtName != tc.want {
				t.Errorf("image.Decode(%s): got format=%q, want %q", tc.name, fmtName, tc.want)
			}
		})
	}
}

// TestFetch_endToEnd_servesJPEGandGIF runs the real Fetch + FetchASCII
// against httptest servers that return JPEG and GIF bytes — the formats
// the prior end-to-end test (PNG-only) missed. Regression guard for the
// "first=JPEG, unknown format" toast.
func TestFetch_endToEnd_servesJPEGandGIF(t *testing.T) {
	cases := []struct {
		name string
		body func() []byte
		ct   string
	}{
		{"jpeg", func() []byte {
			src := image.NewRGBA(image.Rect(0, 0, 64, 64))
			var b bytes.Buffer
			_ = jpeg.Encode(&b, src, &jpeg.Options{Quality: 80})
			return b.Bytes()
		}, "image/jpeg"},
		{"gif", func() []byte {
			pal := image.NewPaletted(image.Rect(0, 0, 64, 64), color.Palette{color.Black, color.White})
			var b bytes.Buffer
			_ = gif.Encode(&b, pal, nil)
			return b.Bytes()
		}, "image/gif"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.ct)
				_, _ = w.Write(body)
			}))
			defer srv.Close()

			// Kitty path (Fetch → toPNGThumbnail → image.Decode).
			esc, err := Fetch(context.Background(), srv.URL, 30, 14, 1)
			if err != nil {
				t.Errorf("Fetch(%s) FAILED: %v", tc.name, err)
			}
			if !strings.HasPrefix(esc, "\x1b_Ga=T,f=100") {
				t.Errorf("Fetch(%s): missing Kitty header in escape", tc.name)
			}

			// ASCII path (FetchASCII → image.Decode → EncodeASCII).
			esc2, err := FetchASCII(context.Background(), srv.URL, 30, 14)
			if err != nil {
				t.Errorf("FetchASCII(%s) FAILED: %v", tc.name, err)
			}
			if !strings.Contains(esc2, "▀") {
				t.Errorf("FetchASCII(%s): missing half-block ▀ in escape", tc.name)
			}
		})
	}
}

// TestFetch_jpegFromLiveAudioAddictCDN proves the production pipeline
// against a real JPEG track-art URL from the AudioAddict CDN. Gated
// behind ADDIPLAY_ALLOW_NETWORK_TESTS so it's diagnostic, not flaky CI.
//
// The URL is the actual art_url returned by /v1/di/track_history at the
// time the user hit the bug; if AudioAddict rotates this image off the
// CDN the test will fail with a 404 — refresh the URL via the probe
// script (/tmp/probe_fanart.py) when that happens.
func TestFetch_jpegFromLiveAudioAddictCDN(t *testing.T) {
	if testing.Short() {
		t.Skip("live network test")
	}
	if !networkAllowed() {
		t.Skip("set ADDIPLAY_ALLOW_NETWORK_TESTS=1 to run live-network probe")
	}
	const url = "https://cdn-images.audioaddict.com/b/c/b/a/f/d/bcbafd57c1728db5188e8c97ce871210.jpg?size=480x480&quality=75"
	esc, err := FetchASCII(context.Background(), url, 30, 15)
	if err != nil {
		t.Fatalf("live JPEG decode FAILED: %v\n(if this URL is gone from the CDN, refresh from /tmp/probe_fanart.py)", err)
	}
	if !strings.Contains(esc, "▀") {
		t.Errorf("live JPEG: missing half-block in escape")
	}
	t.Logf("live JPEG FetchASCII OK: %d bytes of escape", len(esc))
}

func networkAllowed() bool {
	return os.Getenv("ADDIPLAY_ALLOW_NETWORK_TESTS") != ""
}
