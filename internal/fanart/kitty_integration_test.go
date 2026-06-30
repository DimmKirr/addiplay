//go:build kitty_integration

// Package fanart — integration tests that exercise the REAL Kitty
// graphics-protocol pipeline by running a real kitty terminal under
// Xvfb, feeding it the bytes our Encode() function produces, taking a
// screenshot via ImageMagick, and verifying the rendered pixels match
// the source image.
//
// This is the only place where "addiplay emits the right bytes" gets
// upgraded to "the bytes actually produce a visible image in Kitty".
// All other tests in this package are byte-level — they validate wire
// format but cannot detect e.g. an off-by-one in the chunked transmit
// header that a real terminal would reject.
//
// Run with:
//
//	go test -tags=kitty_integration -run TestKitty_renders ./internal/fanart/
//
// Required tools on PATH (or pinned via the test's pathFor* helpers):
//   - kitty   (the terminal — must support graphics protocol; 0.30+)
//   - Xvfb    (xorg-server) — headless X server
//   - magick  (ImageMagick 7) — captures the X11 root window
//   - libGL   (Mesa swrast is fine; LIBGL_ALWAYS_SOFTWARE=1 used)
package fanart

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestKitty_rendersEncodeOutput is the gold-standard end-to-end test
// for the Kitty graphics protocol. Steps:
//
//  1. Build a 64×64 magenta PNG.
//  2. Run it through fanart.Encode() — produces the actual escape
//     sequence addiplay would send to a real terminal.
//  3. Spin up Xvfb, launch real `kitty` against it.
//  4. Have kitty's shell write our Encode() bytes directly to its TTY
//     (via a `printf` of the hex-encoded bytes).
//  5. Screenshot the X11 root with ImageMagick.
//  6. Decode the screenshot and assert at least one pixel matches the
//     source magenta within a small tolerance.
//
// If the byte format is wrong, kitty would either reject silently or
// render a glitched image — either way the magenta pixels would be
// absent and the test fails.
func TestKitty_rendersEncodeOutput(t *testing.T) {
	requireBinaries(t, "kitty", "Xvfb", "magick")

	// 1. Source PNG — pick a color unlikely to appear by accident.
	const (
		srcR uint8 = 220
		srcG uint8 = 40
		srcB uint8 = 200
	)
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			src.Set(x, y, color.RGBA{R: srcR, G: srcG, B: srcB, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}

	// 2. Encode via the function under test. We deliberately disable
	// the tmux wrap here — Xvfb runs no tmux, the bytes go straight
	// to kitty's TTY.
	t.Setenv("TERM", "xterm-kitty")
	escape := Encode(pngBuf.Bytes(), 10, 5, 42)
	if !strings.Contains(escape, "\x1b_Ga=T") {
		t.Fatalf("Encode produced unexpected output: %q", escape[:min(80, len(escape))])
	}

	// 3+4+5. Orchestrate Xvfb + kitty + screenshot. Done in a helper
	// so cleanup is centralised.
	shot := runKittyAndScreenshot(t, escape)

	// 6. Scan the screenshot for any pixel matching our source color
	// within a small tolerance (Xvfb/kitty colour-correct slightly).
	if !screenshotContainsColor(shot, srcR, srcG, srcB, 20) {
		// Save the screenshot to a deterministic path so a developer
		// can eyeball it after a failure.
		dump := filepath.Join(t.TempDir(), "shot-fail.png")
		_ = png.Encode(mustCreate(t, dump), shot)
		t.Errorf("kitty screenshot has no pixel within tolerance of source magenta (%d,%d,%d)\nshot saved to %s",
			srcR, srcG, srcB, dump)
	}
}

// runKittyAndScreenshot stands up Xvfb, launches kitty against it with
// a one-shot shell that feeds `escape` to its own TTY, waits for a
// render, captures the screen, and tears everything down. Returns the
// decoded PNG.
func runKittyAndScreenshot(t *testing.T, escape string) image.Image {
	t.Helper()
	tmp := t.TempDir()
	escapeFile := filepath.Join(tmp, "kitty-escape.bin")
	if err := os.WriteFile(escapeFile, []byte(escape), 0o600); err != nil {
		t.Fatalf("write escape: %v", err)
	}
	shotFile := filepath.Join(tmp, "shot.png")

	// Use a high display number to avoid collisions with anything
	// running on the host. The session-bus mocks placate kitty's
	// startup probes (kitty crashes after render without them, but
	// the render happens first — see kitty.log if this test breaks).
	display := ":108"
	xvfb := exec.Command("Xvfb", display, "-screen", "0", "800x600x24",
		"+extension", "GLX", "+extension", "RENDER")
	xvfb.Env = append(os.Environ())
	xvfbStderr, _ := xvfb.StderrPipe()
	if err := xvfb.Start(); err != nil {
		t.Fatalf("start Xvfb: %v", err)
	}
	t.Cleanup(func() {
		_ = xvfb.Process.Kill()
		_, _ = xvfb.Process.Wait()
		_ = xvfbStderr
	})
	// Give Xvfb a beat to come up before kitty connects.
	time.Sleep(800 * time.Millisecond)

	kittyEnv := append(os.Environ(),
		"DISPLAY="+display,
		"LIBGL_ALWAYS_SOFTWARE=1",
		"HOME="+tmp,
	)
	// `cat` the escape file into kitty's TTY, then sleep so the image
	// stays visible while we screenshot.
	kitty := exec.Command("kitty",
		"--hold",
		"--override", "remember_window_size no",
		"--override", "background #000000",
		"sh", "-c", fmt.Sprintf("cat %s; sleep 8", escapeFile))
	kitty.Env = kittyEnv
	kittyLog := filepath.Join(tmp, "kitty.log")
	logf, _ := os.Create(kittyLog)
	kitty.Stdout = logf
	kitty.Stderr = logf
	if err := kitty.Start(); err != nil {
		t.Fatalf("start kitty: %v", err)
	}
	t.Cleanup(func() {
		_ = kitty.Process.Kill()
		_, _ = kitty.Process.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(kittyLog)
			t.Logf("kitty.log:\n%s", b)
		}
	})
	// Wait for kitty to actually render. 3s is the minimum that
	// consistently captures the magenta pixels on a slow CI box;
	// shorter intervals catch only the splash background.
	time.Sleep(3 * time.Second)

	shot := exec.Command("magick", "import", "-window", "root", shotFile)
	shot.Env = append(os.Environ(), "DISPLAY="+display)
	out, err := shot.CombinedOutput()
	if err != nil {
		t.Fatalf("magick import: %v\noutput: %s", err, out)
	}

	f, err := os.Open(shotFile)
	if err != nil {
		t.Fatalf("open shot: %v", err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode shot: %v", err)
	}
	return img
}

// screenshotContainsColor returns true if any pixel in img is within
// `tolerance` (per channel, absolute) of (r,g,b).
func screenshotContainsColor(img image.Image, r, g, b uint8, tolerance int) bool {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pr, pg, pb, _ := img.At(x, y).RGBA()
			// RGBA() returns 16-bit channels; collapse to 8.
			pr8 := uint8(pr >> 8)
			pg8 := uint8(pg >> 8)
			pb8 := uint8(pb >> 8)
			if abs(int(pr8)-int(r)) <= tolerance &&
				abs(int(pg8)-int(g)) <= tolerance &&
				abs(int(pb8)-int(b)) <= tolerance {
				return true
			}
		}
	}
	return false
}

func requireBinaries(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("integration test needs %q on PATH (not found: %v)", n, err)
		}
	}
}

func mustCreate(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// TestKitty_inTmux_rendersWithPassthrough is the visual repro for
// DIMM-420 #2. It runs kitty under Xvfb, launches tmux inside kitty
// with `allow-passthrough on`, and feeds our Encode() output (which —
// with TERM=tmux-256color set — emits the `\x1bPtmux;...\x1b\\`
// wrapper around each Kitty APC chunk) through the tmux pipeline.
//
// If the tmux wrap is correct, tmux unwraps the chunks and forwards
// the inner `\x1b_G...` to kitty, kitty renders, and the source
// magenta appears in the screenshot.
//
// Without the wrap (revert tmuxwrap.go to a no-op), tmux silently
// eats the APC and no magenta appears — proving this test catches the
// real bug.
func TestKitty_inTmux_rendersWithPassthrough(t *testing.T) {
	requireBinaries(t, "kitty", "Xvfb", "magick", "tmux")

	const (
		srcR uint8 = 220
		srcG uint8 = 40
		srcB uint8 = 200
	)
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			src.Set(x, y, color.RGBA{R: srcR, G: srcG, B: srcB, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}

	// THIS is the line under test: with TERM=tmux-256color, Encode
	// must wrap each chunk in \x1bPtmux;...\x1b\\ — otherwise tmux
	// will eat the bare APC and no image will render.
	t.Setenv("TERM", "tmux-256color")
	escape := Encode(pngBuf.Bytes(), 10, 5, 42)
	if !strings.Contains(escape, "\x1bPtmux;") {
		t.Logf("WARNING: Encode produced no tmux wrapper — visual layer should fail. first 100 bytes: %q", escape[:min(100, len(escape))])
	}

	shot := runKittyTmuxAndScreenshot(t, escape)
	if !screenshotContainsColor(shot, srcR, srcG, srcB, 20) {
		dump := filepath.Join(t.TempDir(), "shot-fail-tmux.png")
		_ = png.Encode(mustCreate(t, dump), shot)
		t.Errorf("kitty+tmux screenshot has no pixel within tolerance of source magenta (%d,%d,%d) — tmux passthrough failed.\nshot saved to %s",
			srcR, srcG, srcB, dump)
	}
}

// runKittyTmuxAndScreenshot stands up Xvfb + kitty + tmux + a shell.
// The shell cats the (already tmux-wrapped) escape so the bytes flow:
//
//	file → cat → tmux pty → tmux unwraps \x1bPtmux;…\x1b\\ → kitty pty
//	     → kitty renders
//
// Returns the screenshot PNG decoded as image.Image.
func runKittyTmuxAndScreenshot(t *testing.T, escape string) image.Image {
	t.Helper()
	tmp := t.TempDir()
	escapeFile := filepath.Join(tmp, "kitty-escape.bin")
	if err := os.WriteFile(escapeFile, []byte(escape), 0o600); err != nil {
		t.Fatalf("write escape: %v", err)
	}
	shotFile := filepath.Join(tmp, "shot.png")

	// Minimal tmux config — passthrough on is the whole point.
	tmuxConf := filepath.Join(tmp, "tmux.conf")
	if err := os.WriteFile(tmuxConf,
		[]byte("set -g allow-passthrough on\nset -g status off\n"), 0o600); err != nil {
		t.Fatalf("write tmux.conf: %v", err)
	}

	display := ":109"
	xvfb := exec.Command("Xvfb", display, "-screen", "0", "800x600x24",
		"+extension", "GLX", "+extension", "RENDER")
	if err := xvfb.Start(); err != nil {
		t.Fatalf("start Xvfb: %v", err)
	}
	t.Cleanup(func() {
		_ = xvfb.Process.Kill()
		_, _ = xvfb.Process.Wait()
	})
	time.Sleep(800 * time.Millisecond)

	kittyEnv := append(os.Environ(),
		"DISPLAY="+display,
		"LIBGL_ALWAYS_SOFTWARE=1",
		"HOME="+tmp,
	)
	// Inside kitty we launch a one-shot tmux session running a shell
	// that cats our pre-tmux-wrapped escape. tmux unwraps and forwards
	// to kitty; kitty renders. Sleep long enough that the screenshot
	// catches it before the session exits.
	innerCmd := fmt.Sprintf("cat %s; sleep 10", escapeFile)
	tmuxCmd := fmt.Sprintf("tmux -f %s new-session -A -s test '%s'", tmuxConf, innerCmd)
	kitty := exec.Command("kitty",
		"--hold",
		"--override", "remember_window_size no",
		"--override", "background #000000",
		"sh", "-c", tmuxCmd)
	kitty.Env = kittyEnv
	kittyLog := filepath.Join(tmp, "kitty.log")
	logf, _ := os.Create(kittyLog)
	kitty.Stdout = logf
	kitty.Stderr = logf
	if err := kitty.Start(); err != nil {
		t.Fatalf("start kitty: %v", err)
	}
	t.Cleanup(func() {
		_ = kitty.Process.Kill()
		_, _ = kitty.Process.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(kittyLog)
			t.Logf("kitty.log:\n%s", b)
		}
	})
	// Slightly longer settle than the no-tmux test — tmux adds ~100ms
	// of init noise on first run.
	time.Sleep(4 * time.Second)

	shot := exec.Command("magick", "import", "-window", "root", shotFile)
	shot.Env = append(os.Environ(), "DISPLAY="+display)
	out, err := shot.CombinedOutput()
	if err != nil {
		t.Fatalf("magick import: %v\noutput: %s", err, out)
	}

	f, err := os.Open(shotFile)
	if err != nil {
		t.Fatalf("open shot: %v", err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode shot: %v", err)
	}
	return img
}
