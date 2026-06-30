package testutil_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/dimmkirr/addiplay/internal/testutil"
)

func TestAAServer_routesAndStatus(t *testing.T) {
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"GET /v1/di/channels":  {Body: `[{"id":1,"name":"Classic Rock"}]`},
		"GET /v1/di/forbidden": {Status: http.StatusForbidden, Body: `{"error":"nope"}`},
	})

	resp, err := http.Get(srv.URL + "/v1/di/channels")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got, want := string(body), `[{"id":1,"name":"Classic Rock"}]`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	resp, err = http.Get(srv.URL + "/v1/di/forbidden")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestMPVFake_recordsCommands(t *testing.T) {
	f := testutil.NewMPVFake(t)
	if f.SocketPath == "" {
		t.Fatal("expected socket path")
	}
	// commands list starts empty
	if got := len(f.Commands()); got != 0 {
		t.Errorf("commands = %d, want 0", got)
	}
}

func TestSkipIfNoLiveCreds_skipsWithoutEnv(t *testing.T) {
	t.Setenv("AA_INTEGRATION_EMAIL", "")
	t.Setenv("AA_INTEGRATION_PASSWORD", "")
	// We can't easily assert t.Skip from inside the same test; use a subtest
	// and verify it's marked skipped.
	t.Run("inner", func(sub *testing.T) {
		testutil.SkipIfNoLiveCreds(sub)
		sub.Fatal("should have skipped")
	})
}
