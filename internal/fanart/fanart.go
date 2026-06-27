// Package fanart fetches a channel's artwork and emits Kitty graphics
// protocol escape sequences so the image renders inline in supporting
// terminals (Ghostty, Kitty, Wezterm, foot). On unsupported terminals
// (xterm, tmux without passthrough, ssh sessions) Supported() returns
// false and the caller skips rendering — there's no ANSI-blocks fallback
// in v0.1.
//
// Reference: https://sw.kovidgoyal.net/kitty/graphics-protocol/
package fanart

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // image.Decode auto-handles .webp after this blank import

	// Format registration for image.Decode. Importing each decoder for its
	// side-effect (init() registers a magic-bytes matcher with the image
	// package). Without these, image.Decode returns "image: unknown
	// format" for JPEG/GIF even when the bytes are well-formed — which
	// was the bug behind the "decode 42031 bytes, first=JPEG, image
	// unknown format" toast: AudioAddict's CDN returns mostly JPEG for
	// track art but we'd only registered PNG (via image/png used above)
	// and WebP.
	_ "image/gif"
	_ "image/jpeg"
)

// Mode describes how (or whether) the current terminal can render fanart.
type Mode int

const (
	// ModeNone — terminal cannot show fanart (no truecolor, no Kitty).
	ModeNone Mode = iota
	// ModeASCII — render via colored Unicode half-blocks (works in any
	// truecolor terminal including tmux). Lower fidelity but universal.
	ModeASCII
	// ModeKitty — Kitty graphics protocol (Ghostty / Kitty / Wezterm /
	// Foot). Real pixels at requested resolution. Best quality.
	ModeKitty
)

func (m Mode) String() string {
	switch m {
	case ModeKitty:
		return "kitty"
	case ModeASCII:
		return "ascii"
	default:
		return "none"
	}
}

// DetectMode picks the best fanart renderer for the current terminal.
//
// Priority: Kitty graphics → ASCII half-blocks → nothing.
//
// tmux awareness: tmux strips Kitty graphics escapes by default, but
// passes truecolor ANSI through. So inside tmux we auto-fall-back to ASCII
// even if the HOST terminal supports Kitty — unless ADDIPLAY_FORCE_FANART=1
// (you have tmux passthrough configured).
//
// User overrides via env:
//
//	ADDIPLAY_NO_FANART=1       — force ModeNone
//	ADDIPLAY_FANART_MODE=ascii — force ASCII even on Kitty-capable terminals
//	ADDIPLAY_FANART_MODE=kitty — force Kitty (combine with ADDIPLAY_FORCE_FANART
//	                        in tmux if your passthrough is configured)
//	ADDIPLAY_FORCE_FANART=1    — bypass tmux Kitty-stripping detection
func DetectMode() Mode {
	if os.Getenv("ADDIPLAY_NO_FANART") != "" {
		return ModeNone
	}
	switch strings.ToLower(os.Getenv("ADDIPLAY_FANART_MODE")) {
	case "ascii":
		if hasTruecolorPassthrough() {
			return ModeASCII
		}
		return ModeNone
	case "kitty":
		return ModeKitty
	case "none":
		return ModeNone
	}

	// Auto-pick: Kitty when supported AND not being stripped by tmux;
	// else ASCII when truecolor passes through; else nothing.
	if hostKittyCapable() && !tmuxStripsGraphics() {
		return ModeKitty
	}
	if hasTruecolorPassthrough() {
		return ModeASCII
	}
	return ModeNone
}

// Supported reports whether ANY fanart mode works. Retained for backward
// compatibility with call sites that just want a yes/no.
func Supported() bool { return DetectMode() != ModeNone }

// hostKittyCapable sniffs env vars for Kitty graphics protocol support
// of the HOST terminal (ignoring whether tmux is sitting in front of it).
func hostKittyCapable() bool {
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return true
	}
	if os.Getenv("GHOSTTY_RESOURCES_DIR") != "" || os.Getenv("GHOSTTY_BIN_DIR") != "" {
		return true
	}
	if tp := os.Getenv("TERM_PROGRAM"); tp == "ghostty" || tp == "WezTerm" {
		return true
	}
	if strings.Contains(os.Getenv("TERM"), "kitty") {
		return true
	}
	return false
}

// inTmux reports whether we're running inside tmux or GNU screen.
func inTmux() bool {
	t := os.Getenv("TERM")
	return strings.HasPrefix(t, "tmux") || strings.HasPrefix(t, "screen")
}

// tmuxStripsGraphics reports whether we should ASSUME tmux will eat the
// Kitty graphics escapes. True inside tmux unless the user explicitly
// opted out by setting ADDIPLAY_FORCE_FANART (they've configured passthrough).
func tmuxStripsGraphics() bool {
	return inTmux() && os.Getenv("ADDIPLAY_FORCE_FANART") == ""
}

// hasTruecolorPassthrough reports whether 24-bit ANSI color reaches the
// host terminal — required for the ASCII half-block fallback. We accept
// either an explicit COLORTERM advertisement OR a known-truecolor host
// terminal env (Ghostty / Kitty / Wezterm / iTerm2), since most users
// don't bother setting COLORTERM in tmux.
func hasTruecolorPassthrough() bool {
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return true
	}
	if hostKittyCapable() {
		// Ghostty / Kitty / Wezterm all do truecolor; ASCII works in tmux
		// on top of them because tmux preserves SGR codes.
		return true
	}
	if tp := os.Getenv("TERM_PROGRAM"); tp == "iTerm.app" || tp == "Apple_Terminal" || tp == "vscode" {
		// iTerm2 + recent Terminal.app + VS Code all do truecolor.
		return true
	}
	return false
}

// debugLog is the package-level diagnostic sink used by Fetch/FetchASCII
// to record URL, content-type, byte length, and the first 32 bytes of
// the body whenever a decode fails. The default io.Discard makes
// SetDebugLogger optional — production sets it from cmd/tui.go when the
// user passes --debug, leaving the toast as the only signal otherwise.
var (
	debugLogMu sync.Mutex
	debugLog   io.Writer = io.Discard
)

// SetDebugLogger installs a writer for fanart diagnostics. Safe to call
// concurrently with Fetch/FetchASCII. Pass io.Discard (or nil) to disable.
func SetDebugLogger(w io.Writer) {
	debugLogMu.Lock()
	defer debugLogMu.Unlock()
	if w == nil {
		w = io.Discard
	}
	debugLog = w
}

func dlog(format string, args ...any) {
	debugLogMu.Lock()
	w := debugLog
	debugLogMu.Unlock()
	_, _ = fmt.Fprintf(w, "[fanart] "+format+"\n", args...)
}

// Cache memoizes encoded Kitty escapes per URL.
type Cache struct {
	mu sync.Mutex
	m  map[string]string
}

// NewCache returns an empty cache.
func NewCache() *Cache { return &Cache{m: map[string]string{}} }

// Get returns the cached escape for url, or "" if not cached.
func (c *Cache) Get(url string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[url]
}

// Put stores escape under url.
func (c *Cache) Put(url, escape string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[url] = escape
}

// magicPrefix returns a short human-readable tag for an image-byte
// prefix — useful in error messages and debug logs to spot when the CDN
// has returned an HTML error page, an AVIF, or something else we don't
// decode.
func magicPrefix(b []byte) string {
	if len(b) == 0 {
		return "empty"
	}
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "PNG"
	case len(b) >= 3 && bytes.Equal(b[:3], []byte{0xff, 0xd8, 0xff}):
		return "JPEG"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "WebP"
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return "GIF"
	case len(b) >= 12 && bytes.Equal(b[4:8], []byte("ftyp")):
		sub := string(b[8:12])
		switch sub {
		case "avif", "avis":
			return "AVIF (no Go stdlib decoder)"
		case "heic", "heix", "mif1":
			return "HEIC/MIF1 (no Go stdlib decoder)"
		}
		return "ISOBMFF/" + sub
	case bytes.HasPrefix(b, []byte("<!DOCTYPE")) || bytes.HasPrefix(b, []byte("<html")) || bytes.HasPrefix(b, []byte("<?xml")):
		return "HTML/XML (likely error page)"
	case bytes.HasPrefix(b, []byte("{")):
		return "JSON (likely error)"
	}
	end := 16
	if len(b) < end {
		end = len(b)
	}
	return fmt.Sprintf("hex=%x", b[:end])
}

// Fetch downloads the image at rawURL, decodes it (PNG / JPEG / WebP),
// downscales to a thumbnail, re-encodes as PNG, and returns a Kitty
// graphics protocol escape sequence to display it at cols×rows cells.
//
// id is a per-channel placement identifier (1..4_294_967_295). Reusing the
// same id for the same image lets Kitty deduplicate; using a fresh id when
// the image changes guarantees a redraw.
//
// For AudioAddict's CDN (cdn-images.audioaddict.com/*.webp), Fetch
// rewrites the URL's ?size= query param to request a small server-side
// thumbnail before we do further downscaling.
func Fetch(ctx context.Context, rawURL string, cols, rows int, id uint32) (string, error) {
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
		dlog("Fetch GET FAIL url=%s err=%v", rawURL, err)
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	ctype := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK {
		dlog("Fetch HTTP %d url=%s content-type=%s", resp.StatusCode, rawURL, ctype)
		return "", fmt.Errorf("fanart: GET %s: %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2 MiB cap
	if err != nil {
		dlog("Fetch read body FAIL url=%s err=%v", rawURL, err)
		return "", err
	}

	pngBytes, err := toPNGThumbnail(body, 320)
	if err != nil {
		dlog("Fetch decode FAIL url=%s content-type=%s bytes=%d magic=%s err=%v",
			rawURL, ctype, len(body), magicPrefix(body), err)
		return "", err
	}
	dlog("Fetch OK url=%s content-type=%s bytes=%d magic=%s",
		rawURL, ctype, len(body), magicPrefix(body))
	return Encode(pngBytes, cols, rows, id), nil
}

// withServerSideThumbnail rewrites a URL to request a smaller image from
// the server when the host honours `?size=WxH&quality=N` (AudioAddict's
// cdn-images.audioaddict.com does). The output keeps any existing aspect
// ratio if present; otherwise it requests a square thumbnail.
func withServerSideThumbnail(rawURL string, maxEdge int) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	// Preserve the aspect ratio of an existing size= param if present.
	w, h := maxEdge, maxEdge
	if existing := q.Get("size"); existing != "" {
		var sw, sh int
		if _, err := fmt.Sscanf(existing, "%dx%d", &sw, &sh); err == nil && sw > 0 && sh > 0 {
			if sw >= sh {
				w = maxEdge
				h = int(float64(maxEdge) * float64(sh) / float64(sw))
			} else {
				h = maxEdge
				w = int(float64(maxEdge) * float64(sw) / float64(sh))
			}
		}
	}
	q.Set("size", fmt.Sprintf("%dx%d", w, h))
	if q.Get("quality") == "" {
		q.Set("quality", "75")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// toPNGThumbnail decodes any supported format (PNG / JPEG / WebP) and
// returns a PNG-encoded thumbnail no larger than maxEdge on its long side.
func toPNGThumbnail(raw []byte, maxEdge int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("fanart: decode (%d bytes, first=%s): %w",
			len(raw), magicPrefix(raw), err)
	}
	dst := scaleToFit(src, maxEdge)
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, dst); err != nil {
		return nil, fmt.Errorf("fanart: png encode: %w", err)
	}
	return out.Bytes(), nil
}

// scaleToFit returns a new image with its long edge at most maxEdge px,
// preserving aspect ratio. If the source is already smaller, it's returned
// unchanged (no upscale).
func scaleToFit(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return src
	}
	var nw, nh int
	if w >= h {
		nw = maxEdge
		nh = h * maxEdge / w
	} else {
		nh = maxEdge
		nw = w * maxEdge / h
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// Encode wraps raw image bytes (PNG / JPEG) into a Kitty graphics escape
// sequence ready to be written to the terminal. It uses the chunked
// transmission form for payloads >4096 bytes.
func Encode(imgBytes []byte, cols, rows int, id uint32) string {
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
		if i == 0 {
			// First chunk: full param set. a=T = transmit AND display now,
			// f=100 = PNG (Kitty auto-detects JPEG too), q=2 = silent.
			fmt.Fprintf(&out, "\x1b_Ga=T,f=100,i=%d,c=%d,r=%d,q=2,m=%d;%s\x1b\\", id, cols, rows, more, chunk)
		} else {
			fmt.Fprintf(&out, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
	return out.String()
}

// FetchASCII downloads + decodes the image at url, downscales to fit
// cols×(rows*2) pixels, and returns a truecolor ANSI rendering using
// Unicode UPPER HALF BLOCK (▀) characters. Each terminal cell shows two
// stacked pixels (FG = top, BG = bottom), doubling effective vertical
// resolution and producing square output pixels.
//
// Works in any terminal advertising truecolor (most modern ones — see
// hasTruecolor in DetectMode). Used as the automatic fallback when
// Kitty graphics are unavailable.
//
// We intentionally don't use quadrant/sextant/octant blocks: they give
// more pixels per cell (4/6/8 vs 2) but force 2-color quantization across
// the cell, producing visibly worse color fidelity on photographic
// content. To squeeze more resolution out, render into MORE cells rather
// than more pixels per cell — callers in ASCII mode get a bigger art
// region (see internal/ui/app.go's asciiArtColumn* constants).
func FetchASCII(ctx context.Context, rawURL string, cols, rows int) (string, error) {
	if rawURL == "" {
		return "", errors.New("fanart: empty url")
	}
	if strings.HasPrefix(rawURL, "//") {
		rawURL = "https:" + rawURL
	}
	rawURL = withServerSideThumbnail(rawURL, 480) // small thumb; we downsample further

	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		dlog("FetchASCII GET FAIL url=%s err=%v", rawURL, err)
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	ctype := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK {
		dlog("FetchASCII HTTP %d url=%s content-type=%s", resp.StatusCode, rawURL, ctype)
		return "", fmt.Errorf("fanart: GET %s: %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		dlog("FetchASCII read body FAIL url=%s err=%v", rawURL, err)
		return "", err
	}

	src, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		dlog("FetchASCII decode FAIL url=%s content-type=%s bytes=%d magic=%s err=%v",
			rawURL, ctype, len(body), magicPrefix(body), err)
		return "", fmt.Errorf("fanart: decode (%d bytes, first=%s): %w",
			len(body), magicPrefix(body), err)
	}
	dlog("FetchASCII OK url=%s content-type=%s bytes=%d magic=%s",
		rawURL, ctype, len(body), magicPrefix(body))
	return EncodeASCII(src, cols, rows), nil
}

// EncodeASCII renders src into cols×rows terminal cells using half-block
// characters. Exported so callers (and tests) can bypass the HTTP fetch.
func EncodeASCII(src image.Image, cols, rows int) string {
	if cols < 1 || rows < 1 {
		return ""
	}
	pixW, pixH := cols, rows*2
	dst := image.NewRGBA(image.Rect(0, 0, pixW, pixH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	var b strings.Builder
	// Pre-size: each cell ≈ 35 bytes of truecolor escape + 1 rune (3 bytes).
	b.Grow(rows * cols * 40)
	for row := 0; row < rows; row++ {
		topY := row * 2
		botY := topY + 1
		for col := 0; col < cols; col++ {
			top := dst.RGBAAt(col, topY)
			bot := dst.RGBAAt(col, botY)
			// FG=top, BG=bot, glyph=▀ (UPPER HALF BLOCK).
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀",
				top.R, top.G, top.B,
				bot.R, bot.G, bot.B)
		}
		// Reset attrs at end of each row so trailing fg/bg don't bleed.
		b.WriteString("\x1b[0m")
		if row < rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Placeholder returns a blank ANSI-coloured block of the requested cell
// dimensions — used when fanart is unsupported OR while the image is
// still loading, so the right column doesn't collapse.
func Placeholder(cols, rows int, bg, fg string) string {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	row := "\x1b[48;2;" + parseRGB(bg) + ";38;2;" + parseRGB(fg) + "m" + strings.Repeat(" ", cols) + "\x1b[0m"
	var b strings.Builder
	for i := 0; i < rows; i++ {
		b.WriteString(row)
		if i < rows-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// parseRGB turns "#rrggbb" into "r;g;b" for ANSI truecolor.
func parseRGB(hex string) string {
	if !strings.HasPrefix(hex, "#") || len(hex) != 7 {
		return "0;0;0"
	}
	var r, g, b int
	_, _ = fmt.Sscanf(hex[1:], "%02x%02x%02x", &r, &g, &b)
	return fmt.Sprintf("%d;%d;%d", r, g, b)
}
