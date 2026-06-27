package fanart

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestPlaceholderBlock_dimensionsAreExact pins the layout contract that
// makes lipgloss + Kitty coexist: the returned block MUST be exactly
// `rows` visible lines tall, each EXACTLY `cols` cells wide. Any drift
// here means cards will mis-align in JoinHorizontal/JoinVertical.
func TestPlaceholderBlock_dimensionsAreExact(t *testing.T) {
	cases := []struct{ cols, rows int }{
		{16, 8},  // current card thumb footprint
		{30, 15}, // Now Playing equivalent
		{1, 1},
		{5, 3},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dx%d", tc.cols, tc.rows), func(t *testing.T) {
			out := placeholderBlock(0xABCDEF, tc.cols, tc.rows)
			lines := strings.Split(out, "\n")
			if len(lines) != tc.rows {
				t.Errorf("got %d rows, want %d", len(lines), tc.rows)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w != tc.cols {
					t.Errorf("row %d: lipgloss.Width=%d, want %d", i, w, tc.cols)
				}
			}
		})
	}
}

// TestPlaceholderBlock_emitsRowColDiacritics is the regression guard
// for the "image tiles vertically inside the card" bug visible in
// image3.jpg. The fix was switching from bare U+10EEEE characters to
// `placeholder + row_diacritic + col_diacritic` per cell — Kitty's
// auto-detect algorithm was treating each formatted row as a separate
// image placement. Explicit diacritics tell Kitty exactly which image
// cell each screen cell belongs to.
//
// If diacritics get removed from placeholderBlock the image tiles
// vertically again; this test catches that immediately.
func TestPlaceholderBlock_emitsRowColDiacritics(t *testing.T) {
	out := placeholderBlock(100, 3, 2)
	// Cell (0,0) should have placeholder + row-0 diacritic (U+0305) +
	// col-0 diacritic (U+0305). Cell (0,1) should have row-0 + col-1
	// (U+030D). Cell (1,0) should have row-1 (U+030D) + col-0 (U+0305).
	// At minimum, both diacritic glyphs must appear in the output.
	if !strings.ContainsRune(out, 0x0305) {
		t.Error("missing U+0305 (row/col index 0 diacritic) — auto-detect mode is back, vertical tiling will reappear")
	}
	if !strings.ContainsRune(out, 0x030D) {
		t.Error("missing U+030D (row/col index 1 diacritic) — explicit cell encoding incomplete")
	}
	// Each cell triple should be placeholder + 2 combining marks.
	cells := strings.Count(out, placeholderChar)
	if cells != 6 {
		t.Errorf("expected 6 placeholder chars for 3x2 block; got %d", cells)
	}
}

// TestPlaceholderBlock_encodesIdInForeground verifies the image id is
// encoded into the SGR truecolor foreground (R=high byte, G=mid byte,
// B=low byte) so Kitty knows which stored image to render in each cell.
// Wrong encoding here means Kitty renders the wrong image (or nothing).
func TestPlaceholderBlock_encodesIdInForeground(t *testing.T) {
	const id uint32 = 0x123456 // R=0x12=18, G=0x34=52, B=0x56=86
	out := placeholderBlock(id, 2, 1)
	want := "\x1b[38;2;18;52;86m"
	if !strings.Contains(out, want) {
		t.Errorf("expected FG color %q in block; got %q", want, out)
	}
}

// TestEncodePlaceholder_includesTransmitAndPlaceholders is the structural
// contract for the EncodePlaceholder return: a Kitty `a=T,U=1` transmit
// followed by the placeholder grid. Both halves matter — only-transmit
// stores the image but never displays it; only-placeholder references a
// stored image that doesn't exist yet.
func TestEncodePlaceholder_includesTransmitAndPlaceholders(t *testing.T) {
	// Build a tiny JPEG so toPNGThumbnail succeeds.
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			src.Set(x, y, color.RGBA{R: 200, G: 40, B: 200, A: 255})
		}
	}
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, src, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	pngBytes, err := toPNGThumbnail(raw.Bytes(), 80)
	if err != nil {
		t.Fatal(err)
	}

	got := EncodePlaceholder(pngBytes, 16, 8, 123)
	// Transmit half — U=1 marks virtual placement so the placeholders
	// are required to actually render the image.
	if !strings.Contains(got, "\x1b_Ga=T,U=1,f=100,i=123,c=16,r=8") {
		t.Errorf("transmit prefix missing/malformed; got start: %q", firstN(got, 80))
	}
	// Placeholder half — at least one U+10EEEE present.
	if !strings.Contains(got, "\U0010EEEE") {
		t.Errorf("placeholder character U+10EEEE missing from output")
	}
}

// TestFetchPlaceholder_endToEnd drives the full HTTP → decode → encode
// pipeline against an httptest JPEG server. Locks in the regression
// guard for the per-card Kitty rendering path.
func TestFetchPlaceholder_endToEnd(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 4), B: 100, A: 255})
		}
	}
	var body bytes.Buffer
	_ = jpeg.Encode(&body, src, &jpeg.Options{Quality: 80})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(body.Bytes())
	}))
	defer srv.Close()

	got, err := FetchPlaceholder(context.Background(), srv.URL, 16, 8, 200)
	if err != nil {
		t.Fatalf("FetchPlaceholder failed: %v", err)
	}
	if !strings.Contains(got, "i=200") {
		t.Errorf("expected i=200 in transmit; got first 120 chars: %q", firstN(got, 120))
	}
	if !strings.Contains(got, "\U0010EEEE") {
		t.Error("missing placeholder character in result")
	}
	// Verify the placeholder block at the end is exactly the right
	// dimensions even after the transmit prefix.
	// Split off transmit (ends at the last \x1b\\ before the FG color
	// for placeholders); take from the first SGR onward.
	idx := strings.Index(got, "\x1b[38;2;")
	if idx == -1 {
		t.Fatal("no SGR FG color in placeholder section")
	}
	block := got[idx:]
	lines := strings.Split(block, "\n")
	if len(lines) != 8 {
		t.Errorf("placeholder block: got %d rows, want 8", len(lines))
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w != 16 {
			t.Errorf("placeholder row %d: width=%d, want 16", i, w)
		}
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
