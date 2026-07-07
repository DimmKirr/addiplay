package fanart

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Kitty's Unicode-placeholder protocol — the layout-safe way to embed
// raster images inside a TUI built from lipgloss-rendered strings.
//
// How it works:
//
//  1. The image is transmitted once with `a=T,U=1,i=N,c=cols,r=rows`.
//     The `U=1` flag marks it as a "virtual placement" — stored in the
//     terminal's image cache but not auto-displayed.
//
//  2. To render the image, the application writes a rectangular grid of
//     U+10EEEE placeholder characters with their FOREGROUND color set
//     to encode the image id (R=high byte, G=mid, B=low byte of id).
//
//  3. Kitty/Ghostty walk the screen looking for contiguous runs of
//     placeholders with the same image-id foreground; each contiguous
//     block displays the stored image.
//
// Why this is layout-safe: each U+10EEEE measures as exactly 1 cell
// wide per go-runewidth (see TestUnicodePlaceholder_isOneCellWide), so
// lipgloss.Width/Height match what the user sees. The image overlays
// the placeholder cells visually but doesn't move the cursor.
//
// Spec: https://sw.kovidgoyal.net/kitty/graphics-protocol/#unicode-placeholders

const placeholderChar = "\U0010EEEE"

// rowColDiacritics is Kitty's standard diacritic table for encoding the
// row and column index of an image cell within a Unicode placeholder.
// Each cell in the placeholder block is `placeholderChar + diacritic(row)
// + diacritic(col)` so the terminal knows EXACTLY which image cell each
// screen cell shows — bypassing the auto-detect "consecutive run"
// algorithm that mis-grouped our 8-row placeholder blocks (the SGR
// foreground reset + newline + SGR foreground set between rows broke
// the contiguous run, causing each row to render a full copy of the
// image and thus tile the thumbnail vertically).
//
// Source (first 32 entries, sufficient for our 16-wide × 8-tall cards
// and Now Playing's 30×15 footprint):
//
//	https://github.com/kovidgoyal/kitty/blob/master/gen/rowcolumn-diacritics.txt
var rowColDiacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
}

// EncodePlaceholder builds a Kitty Unicode-placeholder string ready to
// embed in any lipgloss layout. Returns a string containing:
//
//   1. The transmit escape (a=T,U=1,...) — stores the image bytes in
//      the terminal's cache under `id`. Idempotent for identical bytes
//      at the same id, so re-emitting per frame costs only bandwidth.
//   2. A rows × cols grid of U+10EEEE characters with the image id
//      encoded into the foreground color so the terminal knows which
//      stored image to overlay on each cell.
//
// `id` must be in [1, 2^24). The low 24 bits travel in the FG color;
// higher bits would require a fourth combining diacritic per cell which
// we don't emit. 1 is reserved by nowplaying.go; cards should use
// 100+ to avoid collisions.
func EncodePlaceholder(imgBytes []byte, cols, rows int, id uint32) string {
	transmit := buildPlaceholderTransmit(imgBytes, id, cols, rows)
	block := placeholderBlock(id, cols, rows)
	dlog("EncodePlaceholder: id=%d cols=%d rows=%d imgBytes=%d transmitLen=%d blockLen=%d",
		id, cols, rows, len(imgBytes), len(transmit), len(block))
	return transmit + block
}

func buildPlaceholderTransmit(imgBytes []byte, id uint32, cols, rows int) string {
	b64 := base64.StdEncoding.EncodeToString(imgBytes)
	const chunkSize = 4096
	var out strings.Builder
	for i := 0; i < len(b64); i += chunkSize {
		end := i + chunkSize
		if end > len(b64) {
			end = len(b64)
		}
		chunk := b64[i:end]
		more := 1
		if end >= len(b64) {
			more = 0
		}
		// Per-chunk tmux wrap (DIMM-420 #2): bare APC escapes get
		// eaten by tmux's parser even with `allow-passthrough on`.
		// Each chunk has to be wrapped independently so tmux can
		// forward the inner Kitty sequence to the host terminal.
		var raw string
		if i == 0 {
			// First chunk: full param block. U=1 enables Unicode-
			// placeholder mode (virtual placement, no auto-display).
			// f=100 = PNG. q=2 = silent (no response from terminal).
			raw = fmt.Sprintf(
				"\x1b_Ga=T,U=1,f=100,i=%d,c=%d,r=%d,q=2,m=%d;%s\x1b\\",
				id, cols, rows, more, chunk)
		} else {
			raw = fmt.Sprintf("\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
		out.WriteString(tmuxWrap(raw))
	}
	return out.String()
}

// placeholderBlock returns a rows-line block of U+10EEEE placeholder
// characters with EXPLICIT row/column diacritics. Each cell is encoded
// as `U+10EEEE + diacritic(row) + diacritic(col)` and the foreground
// color encodes the image id (R=high byte, G=mid, B=low byte).
//
// The diacritics matter: without them, Kitty's auto-detect algorithm
// looks for contiguous runs of same-id placeholders and treats each
// uninterrupted run as a single image placement. lipgloss inserts SGR
// resets and rejoins lines, so each row of placeholders becomes its own
// run — making the image tile vertically (8 copies stacked in an 8-row
// thumb, which is exactly the bug image3.jpg showed). Explicit row/col
// diacritics let Kitty place each cell at the correct image coordinate
// regardless of formatting between cells.
//
// Combining diacritics report width 0 in go-runewidth so each cell still
// measures 1 cell wide for lipgloss layout math.
//
// Each line is reset with SGR 0 so trailing fg color doesn't bleed past
// the block.
func placeholderBlock(id uint32, cols, rows int) string {
	r := byte((id >> 16) & 0xFF)
	g := byte((id >> 8) & 0xFF)
	b := byte(id & 0xFF)
	fgSet := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	const reset = "\x1b[0m"
	// Per-cell budget: U+10EEEE (4 bytes UTF-8) + 2 combining diacritics
	// (~2 bytes each) ≈ 8 bytes per cell.
	var sb strings.Builder
	sb.Grow(rows * (len(fgSet) + cols*8 + len(reset) + 1))
	for row := 0; row < rows; row++ {
		sb.WriteString(fgSet)
		for col := 0; col < cols; col++ {
			sb.WriteString(placeholderChar)
			// Defensive bounds: if rows/cols exceed our diacritic table
			// (>=32), fall through to no-diacritic auto-detect for those
			// cells. Should never trigger at current sizes (cards 16×8,
			// Now Playing 30×15).
			if row < len(rowColDiacritics) {
				sb.WriteRune(rowColDiacritics[row])
			}
			if col < len(rowColDiacritics) {
				sb.WriteRune(rowColDiacritics[col])
			}
		}
		sb.WriteString(reset)
		if row < rows-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// FetchPlaceholder downloads an image and returns its Kitty Unicode-
// placeholder rendering (transmit escape + placeholder block). Use when
// the image must compose inside a lipgloss layout (per-card thumbnails,
// inline glyphs, etc.) rather than being placed at a free cursor
// position.
//
// `id` must be unique per concurrent image on screen and in [1, 2^24).
// Re-emitting the returned string per frame is safe: Kitty's re-
// transmission of identical bytes at the same id is a noop.
func FetchPlaceholder(ctx context.Context, rawURL string, cols, rows int, id uint32) (string, error) {
	if rawURL == "" {
		return "", errors.New("fanart: empty url")
	}
	if strings.HasPrefix(rawURL, "//") {
		rawURL = "https:" + rawURL
	}
	rawURL = withServerSideThumbnail(rawURL, 320)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		dlog("FetchPlaceholder GET FAIL url=%s err=%v", rawURL, err)
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	ctype := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK {
		dlog("FetchPlaceholder HTTP %d url=%s content-type=%s", resp.StatusCode, rawURL, ctype)
		return "", fmt.Errorf("fanart: GET %s: %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		dlog("FetchPlaceholder read body FAIL url=%s err=%v", rawURL, err)
		return "", err
	}

	pngBytes, err := toPNGThumbnail(body, 320)
	if err != nil {
		dlog("FetchPlaceholder decode FAIL url=%s content-type=%s bytes=%d magic=%s err=%v",
			rawURL, ctype, len(body), magicPrefix(body), err)
		return "", err
	}
	dlog("FetchPlaceholder OK url=%s content-type=%s bytes=%d magic=%s id=%d",
		rawURL, ctype, len(body), magicPrefix(body), id)
	return EncodePlaceholder(pngBytes, cols, rows, id), nil
}
