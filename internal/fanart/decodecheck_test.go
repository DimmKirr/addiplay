package fanart

import (
	"bytes"
	"context"
	"image"
	"os"
	"testing"
)

// TestLiveDecode_savedWebP rules out "webp blank import not linked":
// if /tmp/test_track.webp exists (saved by the probe script), try to
// decode it through both image.Decode and the full FetchASCII pipeline
// pointed at a live URL. Skipped when the fixture isn't present so this
// stays a diagnostic, not a CI gate.
func TestLiveDecode_savedWebP(t *testing.T) {
	body, err := os.ReadFile("/tmp/test_track.webp")
	if err != nil {
		t.Skipf("no /tmp/test_track.webp fixture: %v", err)
	}
	cfg, fmtName, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		t.Errorf("image.DecodeConfig failed (webp decoder not linked?): %v", err)
	} else {
		t.Logf("DecodeConfig: format=%s size=%dx%d", fmtName, cfg.Width, cfg.Height)
	}
	_, fmtName2, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		t.Errorf("image.Decode failed: %v", err)
	} else {
		t.Logf("Decode: format=%s", fmtName2)
	}
}

// TestLiveFetch_userExampleURL runs the production fanart pipeline
// against the same URL the user pasted earlier; success proves the
// "unknown format" toast can't come from THIS URL — pointing the
// finger at whatever specific track the user is on instead.
func TestLiveFetch_userExampleURL(t *testing.T) {
	if os.Getenv("ADDIPLAY_ALLOW_NETWORK_TESTS") == "" {
		t.Skip("set ADDIPLAY_ALLOW_NETWORK_TESTS=1 to run live-network probe")
	}
	const url = "https://cdn-images.audioaddict.com/0/7/7/c/f/5/077cf5ef3542e5ca4c811ef6edf6e8f7.webp?size=217x217"
	esc, err := FetchASCII(context.Background(), url, 30, 15)
	if err != nil {
		t.Fatalf("FetchASCII against user-pasted URL FAILED: %v", err)
	}
	t.Logf("FetchASCII OK: %d bytes of escape", len(esc))
}
