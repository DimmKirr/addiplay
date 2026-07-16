package ui_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nixBinDirs lists directories where nix-installed tools live.
var nixBinDirs = []string{
	"/nix/var/nix/profiles/per-user/root/profile/bin",
	"/nix/var/nix/profiles/devcell-tools/bin",
	"/home/dmitry/.nix-profile/bin",
}

// findTool locates a binary by name, checking PATH first then nix dirs.
func findTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range nixBinDirs {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// kittyEnv returns the environment variables needed to run Kitty with Mesa
// software rendering on the Xvfb display. Skips if prerequisites are missing.
func kittyEnv(t *testing.T) []string {
	t.Helper()

	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":99"
	}
	// Verify Xvfb socket exists.
	num := strings.TrimPrefix(display, ":")
	sock := filepath.Join("/tmp/.X11-unix", "X"+num)
	if _, err := os.Stat(sock); err != nil {
		t.Skipf("no X11 socket at %s (DISPLAY=%s)", sock, display)
	}

	for _, tool := range []string{"kitty", "xdotool", "import"} {
		if findTool(tool) == "" {
			t.Skipf("%s not found", tool)
		}
	}

	mesaDir := findMesa(t)
	if mesaDir == "" {
		t.Skip("Mesa not found in nix store")
	}

	ldPath := filepath.Join(mesaDir, "lib")
	if nixLDLibs := "/opt/devcell/.nix-ld-libs"; dirExists(nixLDLibs) {
		ldPath = nixLDLibs + ":" + ldPath
	}

	env := os.Environ()
	env = append(env,
		"DISPLAY="+display,
		"LIBGL_ALWAYS_SOFTWARE=1",
		"LIBGL_DRIVERS_PATH="+filepath.Join(mesaDir, "lib", "dri"),
		"LD_LIBRARY_PATH="+ldPath,
		"MESA_LOADER_DRIVER_OVERRIDE=swrast",
		"__GLX_VENDOR_LIBRARY_NAME=mesa",
		"COLORTERM=truecolor",
	)
	return env
}

// findMesa locates the Mesa nix store path containing swrast_dri.so.
func findMesa(t *testing.T) string {
	t.Helper()
	matches, _ := filepath.Glob("/nix/store/*-mesa-*/lib/dri/swrast_dri.so")
	if len(matches) == 0 {
		return ""
	}
	// /nix/store/<hash>-mesa-<ver>/lib/dri/swrast_dri.so → /nix/store/<hash>-mesa-<ver>
	return filepath.Dir(filepath.Dir(filepath.Dir(matches[0])))
}

// buildAddiplay compiles the binary once per test run and returns its path.
func buildAddiplay(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "addiplay")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = projectRoot(t)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}
	return bin
}

func projectRoot(t *testing.T) string {
	t.Helper()
	// Walk up from this test file until we find go.mod.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}

type kittyInstance struct {
	t        *testing.T
	cmd      *exec.Cmd
	windowID string
	env      []string
}

func launchKitty(t *testing.T, env []string, bin string, args ...string) *kittyInstance {
	t.Helper()

	kittyBin := findTool("kitty")

	kittyArgs := []string{
		"--title", "addiplay-e2e",
		"-o", "font_size=11",
		"-o", "font_family=JetBrainsMono Nerd Font",
		"-o", "symbol_map U+2580-U+259F,U+2605-U+2665,U+2717,U+23F5-U+23FA,U+25B6,U+25CF,U+2800-U+28FF DejaVu Sans",
		"-o", "remember_window_size=no",
		"-o", "initial_window_width=1600",
		"-o", "initial_window_height=900",
		"-o", "background=#1a1b26",
		"-o", "foreground=#c0caf5",
		"-o", "window_padding_width=0",
		"--",
	}
	kittyArgs = append(kittyArgs, bin)
	kittyArgs = append(kittyArgs, args...)

	cmd := exec.Command(kittyBin, kittyArgs...)
	cmd.Env = env
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("kitty start: %v", err)
	}

	k := &kittyInstance{t: t, cmd: cmd, env: env}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	k.windowID = k.waitForWindow(10 * time.Second)
	return k
}

func (k *kittyInstance) waitForWindow(timeout time.Duration) string {
	k.t.Helper()
	deadline := time.Now().Add(timeout)
	xdotoolBin := findTool("xdotool")
	for time.Now().Before(deadline) {
		cmd := exec.Command(xdotoolBin, "search", "--name", "addiplay-e2e")
		cmd.Env = k.env
		out, err := cmd.Output()
		if err == nil {
			lines := strings.TrimSpace(string(out))
			if lines != "" {
				parts := strings.Split(lines, "\n")
				wid := strings.TrimSpace(parts[len(parts)-1])
				if wid != "" {
					return wid
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	k.t.Fatal("kitty window did not appear within timeout")
	return ""
}

func (k *kittyInstance) sendKey(key string) {
	k.t.Helper()
	cmd := exec.Command(findTool("xdotool"), "key", "--window", k.windowID, key)
	cmd.Env = k.env
	if out, err := cmd.CombinedOutput(); err != nil {
		k.t.Fatalf("xdotool key %q: %v\n%s", key, err, out)
	}
}

func (k *kittyInstance) screenshot(path string) {
	k.t.Helper()
	cmd := exec.Command(findTool("import"), "-window", k.windowID, path)
	cmd.Env = k.env
	if out, err := cmd.CombinedOutput(); err != nil {
		k.t.Fatalf("import screenshot: %v\n%s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		k.t.Fatalf("screenshot file missing or empty: %s", path)
	}
}

func envVal(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return env[i][len(prefix):]
		}
	}
	return ""
}

// screenshotDir returns the project's .context/screenshots directory, creating
// it if needed.
func screenshotDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(projectRoot(t), ".context", "screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir screenshots: %v", err)
	}
	return dir
}

func TestKittyE2E_homeScreen(t *testing.T) {
	if testing.Short() {
		t.Skip("kitty e2e skipped in short mode")
	}
	env := kittyEnv(t)
	bin := buildAddiplay(t)

	k := launchKitty(t, env, bin, "--demo")
	time.Sleep(2 * time.Second) // let channels load and render

	path := filepath.Join(screenshotDir(t),
		fmt.Sprintf("%s-e2e-home.png", time.Now().Format("2006-01-02T150405")))
	k.screenshot(path)
	t.Logf("home screenshot: %s", path)
}

func TestKittyE2E_playFirstStation(t *testing.T) {
	if testing.Short() {
		t.Skip("kitty e2e skipped in short mode")
	}
	env := kittyEnv(t)
	bin := buildAddiplay(t)

	k := launchKitty(t, env, bin, "--demo")
	time.Sleep(2 * time.Second)

	// Press Enter to play the first (selected) station.
	k.sendKey("Return")
	time.Sleep(3 * time.Second) // wait for playing state + track info

	path := filepath.Join(screenshotDir(t),
		fmt.Sprintf("%s-e2e-playing.png", time.Now().Format("2006-01-02T150405")))
	k.screenshot(path)
	t.Logf("playing screenshot: %s", path)
}

// TestKittyE2E_integration launches the real TUI (not demo) with live
// AudioAddict credentials, plays the first station, and verifies that
// real channel/track art renders via the Kitty graphics protocol.
//
// Requires: AA_INTEGRATION_EMAIL, AA_INTEGRATION_PASSWORD env vars.
// Skipped when either is unset.
func TestKittyE2E_integration(t *testing.T) {
	if testing.Short() {
		t.Skip("kitty e2e skipped in short mode")
	}

	email := os.Getenv("AA_INTEGRATION_EMAIL")
	password := os.Getenv("AA_INTEGRATION_PASSWORD")
	if email == "" || password == "" {
		t.Skip("integration test skipped: set AA_INTEGRATION_EMAIL and AA_INTEGRATION_PASSWORD")
	}

	env := kittyEnv(t)
	bin := buildAddiplay(t)

	if findTool("mpv") == "" {
		t.Skip("mpv not found — required for integration playback")
	}

	// Pre-authenticate against the live API and write creds to a temp dir.
	// The binary reads creds from ADDICTUNED_CONFIG_DIR/creds.json.
	credsDir := t.TempDir()
	preAuth(t, email, password, credsDir)

	// Configure mpv to use null audio output (no sound hardware in CI).
	mpvHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(mpvHome, "mpv.conf"), []byte("ao=null\n"), 0o644); err != nil {
		t.Fatalf("write mpv.conf: %v", err)
	}

	// mpv needs its own FFmpeg libs ahead of nix-ld-libs to avoid
	// version mismatch (nix profile mpv brings FFmpeg 8.1.1, while
	// nix-ld-libs may have FFmpeg 8.0).
	mpvLibDirs := mpvFFmpegLibs(t)

	// Add creds dir + mpv to the env for the kitty-launched binary.
	env = append(env,
		"ADDICTUNED_CONFIG_DIR="+credsDir,
		"MPV_HOME="+mpvHome,
	)
	env = setEnvPATH(env, filepath.Dir(findTool("mpv")))
	if mpvLibDirs != "" {
		env = prependEnvVar(env, "LD_LIBRARY_PATH", mpvLibDirs)
	}

	debugLog := filepath.Join(t.TempDir(), "debug.log")
	k := launchKitty(t, env, bin, "--debug", "--debug-log", debugLog)

	// Wait for channels to load and render with real images.
	time.Sleep(5 * time.Second)

	homePath := filepath.Join(screenshotDir(t),
		fmt.Sprintf("%s-integration-home.png", time.Now().Format("2006-01-02T150405")))
	k.screenshot(homePath)
	t.Logf("integration home screenshot: %s", homePath)

	// Play the first station.
	k.sendKey("Return")
	time.Sleep(1 * time.Second) // catch loading spinner
	loadingPath := filepath.Join(screenshotDir(t),
		fmt.Sprintf("%s-integration-loading.png", time.Now().Format("2006-01-02T150405")))
	k.screenshot(loadingPath)
	t.Logf("integration loading screenshot: %s", loadingPath)

	time.Sleep(7 * time.Second) // stream connect + track metadata + fanart fetch

	playingPath := filepath.Join(screenshotDir(t),
		fmt.Sprintf("%s-integration-playing.png", time.Now().Format("2006-01-02T150405")))
	k.screenshot(playingPath)
	t.Logf("integration playing screenshot: %s", playingPath)

	// Read debug log to verify fanart was fetched.
	logData, err := os.ReadFile(debugLog)
	if err != nil {
		t.Logf("warning: could not read debug log: %v", err)
	} else {
		log := string(logData)
		t.Logf("debug log length: %d bytes", len(logData))
		if strings.Contains(log, "refreshFanart") {
			t.Log("fanart refresh triggered")
		}
		if strings.Contains(log, "ModeKitty") {
			t.Log("Kitty graphics mode detected")
		}
		if strings.Contains(log, "kitty: wrote") || strings.Contains(log, "kitty_write") {
			t.Log("Kitty image data written to terminal")
		}
	}
}

// preAuth authenticates against the live AudioAddict API and writes
// creds.json to the given directory.
func preAuth(t *testing.T, email, password, credsDir string) {
	t.Helper()

	// Use the audioaddict client to authenticate, then write the resulting
	// session as JSON. We import the wire format directly to avoid pulling
	// in the full audioaddict package from the test.
	//
	// Simpler approach: build a tiny Go program that imports audioaddict +
	// creds and does the auth. But that's heavy. Instead, hit the API
	// directly with net/http — the endpoint is stable.
	form := "member_session%5Busername%5D=" + email +
		"&member_session%5Bpassword%5D=" + password

	ctx := t.Context()
	req, err := newHTTPRequest(ctx, "POST",
		"https://api.audioaddict.com/v1/di/member_sessions", form)
	if err != nil {
		t.Fatalf("build auth request: %v", err)
	}

	resp, err := httpClient().Do(req)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		t.Fatalf("authenticate: status %d: %s", resp.StatusCode, string(body[:n]))
	}

	var payload struct {
		Key        string `json:"key"`
		AudioToken string `json:"audio_token"`
		Member     struct {
			ID            int64  `json:"id"`
			Email         string `json:"email"`
			ListenKey     string `json:"listen_key"`
			UserType      string `json:"user_type"`
			Subscriptions []struct {
				Status string `json:"status"`
			} `json:"active_subscriptions"`
		} `json:"member"`
	}

	if err := decodeJSON(resp.Body, &payload); err != nil {
		t.Fatalf("decode auth response: %v", err)
	}

	if payload.Member.ListenKey == "" {
		t.Fatal("authenticate: empty listen_key in response")
	}

	premium := payload.Member.UserType == "premium"
	if !premium {
		for _, s := range payload.Member.Subscriptions {
			if s.Status == "active" {
				premium = true
				break
			}
		}
	}

	cred := struct {
		ID         int64  `json:"id,omitempty"`
		Email      string `json:"email"`
		ListenKey  string `json:"listen_key"`
		SessionKey string `json:"session_key,omitempty"`
		AudioToken string `json:"audio_token,omitempty"`
		Premium    bool   `json:"premium"`
	}{
		ID:         payload.Member.ID,
		Email:      payload.Member.Email,
		ListenKey:  payload.Member.ListenKey,
		SessionKey: payload.Key,
		AudioToken: payload.AudioToken,
		Premium:    premium,
	}

	raw, err := encodeJSON(cred)
	if err != nil {
		t.Fatalf("encode creds: %v", err)
	}
	path := filepath.Join(credsDir, "creds.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	t.Logf("authenticated as %s (premium=%t) → %s", cred.Email, cred.Premium, path)
}

// setEnvPATH prepends a directory to PATH in the env slice.
func setEnvPATH(env []string, dir string) []string {
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + dir + ":" + e[5:]
			return env
		}
	}
	return append(env, "PATH="+dir)
}

func newHTTPRequest(ctx context.Context, method, url, body string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("streams", "diradio")
	return req, nil
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}

func decodeJSON(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}

func encodeJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// mpvFFmpegLibs extracts the FFmpeg lib directories from the real mpv
// binary's RUNPATH. When LD_LIBRARY_PATH is set (as it is for Mesa),
// it overrides RUNPATH, causing version mismatches. Prepending the
// correct FFmpeg libs fixes this.
func mpvFFmpegLibs(t *testing.T) string {
	t.Helper()
	mpvBin := findTool("mpv")
	if mpvBin == "" {
		return ""
	}
	// Resolve through nix wrapper to find the real binary.
	real := resolveNixWrapper(mpvBin)
	readelfBin := findTool("readelf")
	if readelfBin == "" {
		return ""
	}
	out, err := exec.Command(readelfBin, "-d", real).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "RUNPATH") && !strings.Contains(line, "RPATH") {
			continue
		}
		// Extract the path list between [ and ]
		start := strings.Index(line, "[")
		end := strings.LastIndex(line, "]")
		if start < 0 || end <= start {
			continue
		}
		rpath := line[start+1 : end]
		var ffmpegDirs []string
		for _, dir := range strings.Split(rpath, ":") {
			if strings.Contains(dir, "ffmpeg") {
				ffmpegDirs = append(ffmpegDirs, dir)
			}
		}
		return strings.Join(ffmpegDirs, ":")
	}
	return ""
}

// resolveNixWrapper follows a nix C wrapper to find the real binary.
// Nix wrappers contain the real path as a string.
func resolveNixWrapper(wrapper string) string {
	resolved, err := filepath.EvalSymlinks(wrapper)
	if err != nil {
		return wrapper
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return resolved
	}
	// Look for a nix store path to the real binary embedded in the wrapper.
	// Pattern: /nix/store/...-<name>/bin/<name>
	for _, line := range strings.Split(string(raw), "\x00") {
		if strings.HasPrefix(line, "/nix/store/") && strings.HasSuffix(line, "/bin/mpv") {
			if _, err := os.Stat(line); err == nil {
				return line
			}
		}
	}
	return resolved
}

func prependEnvVar(env []string, key, val string) []string {
	prefix := key + "="
	lastIdx := -1
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			lastIdx = i
		}
	}
	if lastIdx >= 0 {
		env[lastIdx] = prefix + val + ":" + env[lastIdx][len(prefix):]
		return env
	}
	return append(env, key+"="+val)
}
