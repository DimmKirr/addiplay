package fanart

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestWithServerSideThumbnail_addsSizeAndQuality(t *testing.T) {
	in := "https://cdn-images.audioaddict.com/9/a/9.webp"
	got := withServerSideThumbnail(in, 320)
	if !strings.Contains(got, "size=320x320") {
		t.Errorf("missing size=320x320 in %s", got)
	}
	if !strings.Contains(got, "quality=75") {
		t.Errorf("missing quality=75 in %s", got)
	}
}

func TestWithServerSideThumbnail_preservesAspect(t *testing.T) {
	in := "https://cdn-images.audioaddict.com/x.webp?size=560x804&quality=75"
	got := withServerSideThumbnail(in, 320)
	// 320 * 560 / 804 = 222, so we expect size=222x320 (tall aspect preserved)
	if !strings.Contains(got, "size=222x320") {
		t.Errorf("expected aspect-preserved size=222x320, got %s", got)
	}
}

func TestWithServerSideThumbnail_preservesQualityIfPresent(t *testing.T) {
	in := "https://x.com/y.webp?quality=42"
	got := withServerSideThumbnail(in, 100)
	if strings.Contains(got, "quality=75") {
		t.Errorf("should not overwrite existing quality; got %s", got)
	}
}

func TestToPNGThumbnail_pngRoundtrip(t *testing.T) {
	// Build a 800x600 red PNG and downscale it.
	src := image.NewRGBA(image.Rect(0, 0, 800, 600))
	red := color.RGBA{R: 255, A: 255}
	for x := 0; x < 800; x++ {
		for y := 0; y < 600; y++ {
			src.Set(x, y, red)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	got, err := toPNGThumbnail(buf.Bytes(), 320)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	b := decoded.Bounds()
	if b.Dx() != 320 || b.Dy() != 240 {
		t.Errorf("scaled size = %dx%d, want 320x240 (preserved 4:3 aspect)", b.Dx(), b.Dy())
	}
}

// TestEncode_kittyProtocolShape verifies the escape sequence we emit
// matches the Kitty graphics protocol (which Ghostty implements). Spec:
// https://sw.kovidgoyal.net/kitty/graphics-protocol/
func TestEncode_kittyProtocolShape(t *testing.T) {
	// 8 KB synthetic payload → 3 chunks at the 4096-byte chunk size.
	raw := bytes.Repeat([]byte{0xAB}, 8192)
	got := Encode(raw, 30, 14, 42)

	// First chunk: full param block with a=T,f=100,i=42,c=30,r=14,q=2,m=1
	wantFirstHeader := "\x1b_Ga=T,f=100,i=42,c=30,r=14,q=2,m=1;"
	if !strings.HasPrefix(got, wantFirstHeader) {
		t.Errorf("first chunk header missing or malformed.\n  want prefix: %q\n  got prefix:  %q", wantFirstHeader, got[:min(len(wantFirstHeader)+20, len(got))])
	}

	// Must end with the ST terminator (ESC \) of the last chunk.
	if !strings.HasSuffix(got, "\x1b\\") {
		t.Errorf("escape missing ST terminator (ESC \\)")
	}

	// Continuation chunks: header is just `\x1b_Gm=1;` (more=1) for middle,
	// `\x1b_Gm=0;` (more=0) for last. We should see at least one m=0.
	if !strings.Contains(got, "\x1b_Gm=0;") {
		t.Errorf("expected a final-chunk header (m=0); got len=%d", len(got))
	}
	if !strings.Contains(got, "\x1b_Gm=1;") {
		t.Errorf("expected at least one continuation header (m=1); got len=%d", len(got))
	}

	// Chunk count check: 8192 raw → ~10925 base64 → 3 chunks of 4096 (3rd is partial).
	headers := strings.Count(got, "\x1b_G")
	terminators := strings.Count(got, "\x1b\\")
	if headers != terminators {
		t.Errorf("header/terminator mismatch: %d headers vs %d ST", headers, terminators)
	}
}

// TestEncode_smallPayloadSingleChunk verifies the typical thumbnail case
// (<4 KB after base64): no chunking, single `m=0` header.
func TestEncode_smallPayloadSingleChunk(t *testing.T) {
	raw := []byte("tiny")
	got := Encode(raw, 30, 14, 7)
	if !strings.HasPrefix(got, "\x1b_Ga=T,f=100,i=7,c=30,r=14,q=2,m=0;") {
		t.Errorf("small payload should emit a single m=0 chunk; got %q", got)
	}
	if strings.Count(got, "\x1b_G") != 1 {
		t.Errorf("small payload should emit exactly 1 chunk; got %d", strings.Count(got, "\x1b_G"))
	}
}

// TestEncodeASCII_dimensionsAreExact is the QA gate for fanart sizing.
// The whole reason ASCII output gets "distorted" in the TUI is when the
// caller asks for N×M cells but renders into a smaller pane: lipgloss
// wraps the over-long lines and the image goes to mush. This test pins
// the contract:
//
//   - Output has EXACTLY rows visible lines (separated by \n)
//   - Each line is EXACTLY cols visible cells wide (lipgloss.Width ignores SGR)
//   - Works regardless of the source image's aspect ratio (we always
//     downscale into the requested cell footprint)
func TestEncodeASCII_dimensionsAreExact(t *testing.T) {
	cases := []struct{ srcW, srcH, cols, rows int }{
		{srcW: 800, srcH: 600, cols: 30, rows: 15},  // 4:3 → fit into roughly-square cells
		{srcW: 217, srcH: 217, cols: 50, rows: 25},  // album-cover square → ASCII square frame
		{srcW: 300, srcH: 280, cols: 50, rows: 25},  // station art → same frame
		{srcW: 200, srcH: 1000, cols: 30, rows: 30}, // tall portrait → must still fit
		{srcW: 10, srcH: 10, cols: 50, rows: 25},    // tiny source must UPscale
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("src=%dx%d→cells=%dx%d", tc.srcW, tc.srcH, tc.cols, tc.rows), func(t *testing.T) {
			src := image.NewRGBA(image.Rect(0, 0, tc.srcW, tc.srcH))
			// Fill with a non-trivial gradient so colors actually vary.
			for y := 0; y < tc.srcH; y++ {
				for x := 0; x < tc.srcW; x++ {
					src.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
				}
			}
			got := EncodeASCII(src, tc.cols, tc.rows)
			lines := strings.Split(got, "\n")
			if len(lines) != tc.rows {
				t.Errorf("got %d rows, want exactly %d", len(lines), tc.rows)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w != tc.cols {
					t.Errorf("row %d: got %d cells wide, want exactly %d (line: %q)", i, w, tc.cols, line)
				}
			}
		})
	}
}

// TestEncodeASCII_zeroDimensions covers the defensive guard.
func TestEncodeASCII_zeroDimensions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 50, 50))
	for _, c := range []struct{ cols, rows int }{{0, 10}, {10, 0}, {-1, 10}, {10, -1}, {0, 0}} {
		if got := EncodeASCII(src, c.cols, c.rows); got != "" {
			t.Errorf("cols=%d rows=%d: expected empty string, got %d bytes", c.cols, c.rows, len(got))
		}
	}
}

func TestFetch_endToEndWithLocalServer(t *testing.T) {
	// 64x64 PNG served by httptest; verify Fetch downloads + scales + encodes.
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, src); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes.Bytes())
	}))
	defer srv.Close()

	escape, err := Fetch(context.Background(), srv.URL, 30, 14, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(escape, "\x1b_Ga=T,f=100,i=1,c=30,r=14") {
		t.Errorf("escape missing Kitty header; got prefix %q", escape[:min(60, len(escape))])
	}
	if !strings.HasSuffix(escape, "\x1b\\") {
		t.Errorf("escape missing terminator")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
