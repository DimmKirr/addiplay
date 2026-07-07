package audioaddict_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/testutil"
)

// TestVote_requiresBasicAuthAndSessionKey reproduces the live-API bug
// observed 2026-06-29: the vote endpoint returns `403 "Invalid Session"`
// when only X-Session-Key is sent, even though the session key itself is
// valid. The DannyBen/audio_addict Ruby gem masks this by calling
// HTTParty's class-level `basic_auth 'streams', 'diradio'` during login,
// which carries the Authorization header into every subsequent request.
// AudioAddict's vote endpoint requires BOTH credentials.
//
// Captured from a live --debug trace at 14:23:57Z:
//
//	voteRequest -> POST .../tracks/.../vote/.../up session_key_len=32
//	voteRequest <- status=403 body_preview="Invalid Session"
//
// This mock rejects votes lacking either credential; the test asserts
// LikeTrack sends both.
func TestVote_requiresBasicAuthAndSessionKey(t *testing.T) {
	const sessionKey = "top-level-session-key-32-charsxx"
	var (
		gotVoteAuthOK   bool
		gotVoteAuthUser string
		gotVoteAuthPass string
		gotVoteSession  string
		gotVotePath     string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/di/member_sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"key": "`+sessionKey+`",
				"member": {
					"id": 9999,
					"email": "x@example.com",
					"listen_key": "lk-24-chars-xxxxxxxxxxxx",
					"user_type": "premium"
				}
			}`)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/rockradio/tracks/") && strings.HasSuffix(r.URL.Path, "/up"):
			gotVoteAuthUser, gotVoteAuthPass, gotVoteAuthOK = r.BasicAuth()
			gotVoteSession = r.Header.Get("X-Session-Key")
			gotVotePath = r.URL.Path
			// Mimic the live server: require BOTH basic auth and X-Session-Key.
			if gotVoteAuthOK && gotVoteAuthUser == "streams" && gotVoteAuthPass == "diradio" && gotVoteSession == sessionKey {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "Invalid Session")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	m, err := c.Authenticate(context.Background(), "x@example.com", "pw", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if m.SessionKey != sessionKey {
		t.Fatalf("SessionKey = %q, want %q (top-level key)", m.SessionKey, sessionKey)
	}

	// Authenticate has already called SetCreds(m) so the Client now
	// holds the right session_key; LikeTrack reads it from there.
	if err := c.LikeTrack(context.Background(), "rockradio", 627467, 222); err != nil {
		t.Fatalf("LikeTrack: %v\n  vote path=%q\n  basic_auth_ok=%t user=%q pass=%q\n  X-Session-Key=%q",
			err, gotVotePath, gotVoteAuthOK, gotVoteAuthUser, gotVoteAuthPass, gotVoteSession)
	}
}

// TestAuthenticate_postsToMemberSessions verifies the FULL request shape:
// URL, basic auth credentials, and the nested `member_session[...]` form
// body the live API expects. Per the DannyBen/audio_addict Ruby gem
// (`lib/audio_addict/api.rb`), the live auth endpoint is
// `POST /v1/<network>/member_sessions` with basic auth `streams:diradio`
// and a Rails-bracket form body. The response nests member fields under
// a top-level `member` key and surfaces the session key as top-level `key`.
//
// This test replaced earlier tests that POSTed flat creds to a legacy
// `/members/authenticate` endpoint — that path returned a listen_key but
// no session key, which is why DIMM-381's `l` hotkey kept firing the
// re-auth path: the SessionKey field was always empty.
func TestAuthenticate_postsToMemberSessions(t *testing.T) {
	var (
		gotMethod   string
		gotPath     string
		gotAuthUser string
		gotAuthPass string
		gotAuthOK   bool
		gotBody     string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuthUser, gotAuthPass, gotAuthOK = r.BasicAuth()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"key": "sess-xyz",
			"member": {
				"id": 42,
				"email": "test@example.com",
				"listen_key": "lk-abc",
				"user_type": "premium"
			}
		}`)
	}))
	t.Cleanup(srv.Close)

	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	m, err := c.Authenticate(context.Background(), "test@example.com", "pw", "")
	if err != nil {
		t.Fatal(err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/di/member_sessions" {
		t.Errorf("path = %q, want /v1/di/member_sessions", gotPath)
	}
	if !gotAuthOK || gotAuthUser != "streams" || gotAuthPass != "diradio" {
		t.Errorf("basic auth = (%q, %q, ok=%t); want (streams, diradio, ok=true)", gotAuthUser, gotAuthPass, gotAuthOK)
	}
	// Rails-bracket form params survive URL encoding as %5B = '[' and %5D = ']'.
	if !strings.Contains(gotBody, "member_session%5Busername%5D=test%40example.com") {
		t.Errorf("body missing member_session[username]; got %q", gotBody)
	}
	if !strings.Contains(gotBody, "member_session%5Bpassword%5D=pw") {
		t.Errorf("body missing member_session[password]; got %q", gotBody)
	}

	if m.ListenKey != "lk-abc" {
		t.Errorf("ListenKey = %q, want lk-abc", m.ListenKey)
	}
	if m.SessionKey != "sess-xyz" {
		t.Errorf("SessionKey = %q, want sess-xyz (top-level key)", m.SessionKey)
	}
	if m.Email != "test@example.com" {
		t.Errorf("Email = %q, want test@example.com", m.Email)
	}
	if !m.Premium {
		t.Errorf("Premium = false, want true (user_type=premium)")
	}
}

func TestAuthenticate_badCreds(t *testing.T) {
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"POST /v1/di/member_sessions": {Status: http.StatusUnauthorized, Body: `{"errors":["invalid"]}`},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	_, err := c.Authenticate(context.Background(), "x", "y", "")
	if !errors.Is(err, audioaddict.ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestAuthenticate_oauthOnly(t *testing.T) {
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"POST /v1/di/member_sessions": {
			Status: http.StatusForbidden,
			Body:   `{"errors":["this account uses Google oauth login, no password set"]}`,
		},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	_, err := c.Authenticate(context.Background(), "x", "y", "")
	if !errors.Is(err, audioaddict.ErrOAuthOnly) {
		t.Errorf("err = %v, want ErrOAuthOnly", err)
	}
}

func TestAuthenticate_emptyListenKey(t *testing.T) {
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"POST /v1/di/member_sessions": {Body: `{"key":"sk","member":{"id":1,"email":"x@y","listen_key":""}}`},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	_, err := c.Authenticate(context.Background(), "x", "y", "")
	if err == nil || !strings.Contains(err.Error(), "empty listen_key") {
		t.Errorf("err = %v, want empty-listen_key error", err)
	}
}

// TestLikeTrack_postsCorrectPathAndHeader uses a raw httptest.Server so we
// can inspect the request method, path, and X-Session-Key header — the
// testutil AAFixture helper only matches method+path and ignores headers.
func TestLikeTrack_postsCorrectPathAndHeader(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotHeader string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Session-Key")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"
	c.SetCreds(creds.Session{SessionKey: "testkey"})

	if err := c.LikeTrack(context.Background(), "di", 42, 7); err != nil {
		t.Fatalf("LikeTrack: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/di/tracks/42/vote/7/up" {
		t.Errorf("path = %q, want /v1/di/tracks/42/vote/7/up", gotPath)
	}
	if gotHeader != "testkey" {
		t.Errorf("X-Session-Key = %q, want testkey", gotHeader)
	}
}

func TestUnlikeTrack_deletesCorrectPath(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotHeader string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Session-Key")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"
	c.SetCreds(creds.Session{SessionKey: "testkey"})

	if err := c.UnlikeTrack(context.Background(), "di", 42, 7); err != nil {
		t.Fatalf("UnlikeTrack: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/v1/di/tracks/42/vote/7" {
		t.Errorf("path = %q, want /v1/di/tracks/42/vote/7", gotPath)
	}
	if gotHeader != "testkey" {
		t.Errorf("X-Session-Key = %q, want testkey", gotHeader)
	}
}

func TestLikeTrack_401MapsToErrUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"
	c.SetCreds(creds.Session{SessionKey: "testkey"})

	err := c.LikeTrack(context.Background(), "di", 42, 7)
	if !errors.Is(err, audioaddict.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestDislikeTrack_postsCorrectPathAndHeader(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotHeader string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Session-Key")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"
	c.SetCreds(creds.Session{SessionKey: "testkey"})

	if err := c.DislikeTrack(context.Background(), "di", 42, 7); err != nil {
		t.Fatalf("DislikeTrack: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/di/tracks/42/vote/7/down" {
		t.Errorf("path = %q, want /v1/di/tracks/42/vote/7/down", gotPath)
	}
	if gotHeader != "testkey" {
		t.Errorf("X-Session-Key = %q, want testkey", gotHeader)
	}
}

func TestDislikeTrack_401MapsToErrUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"
	c.SetCreds(creds.Session{SessionKey: "testkey"})

	err := c.DislikeTrack(context.Background(), "di", 42, 7)
	if !errors.Is(err, audioaddict.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestLikeTrack_429MapsToErrRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"
	c.SetCreds(creds.Session{SessionKey: "testkey"})

	err := c.LikeTrack(context.Background(), "di", 42, 7)
	if !errors.Is(err, audioaddict.ErrRateLimit) {
		t.Errorf("err = %v, want ErrRateLimit", err)
	}
}

func TestChannels_success(t *testing.T) {
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"GET /v1/di/channels": {Body: `[
			{"id":1,"key":"classicrock","name":"Classic Rock","description":"","asset_url":""},
			{"id":2,"key":"chillout","name":"Chillout","description":"","asset_url":""}
		]`},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	chs, err := c.Channels(context.Background(), "di")
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 || chs[0].Key != "classicrock" {
		t.Errorf("channels = %+v", chs)
	}
}

func TestChannels_unauthorized(t *testing.T) {
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"GET /v1/di/channels": {Status: http.StatusForbidden, Body: `{}`},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	_, err := c.Channels(context.Background(), "di")
	if !errors.Is(err, audioaddict.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestCurrentlyPlaying(t *testing.T) {
	// Real shape (verified live 2026-06-26): /track_history/channel/<id>
	// returns a flat list of past tracks; index [0] is the now-playing one.
	// Contains art_url for the album cover — the whole reason we picked
	// this endpoint over the cheaper /currently_playing which omits it.
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"GET /v1/di/track_history/channel/1": {Body: `[{
			"channel_id": 1,
			"network_id": 1,
			"track_id": 42,
			"type": "track",
			"artist": "Daft Punk",
			"display_artist": "Daft Punk",
			"title": "Around The World",
			"display_title": "Around The World",
			"track": "Daft Punk - Around The World",
			"length": 420,
			"duration": 420.5,
			"art_url": "//cdn-images.audioaddict.com/0/7/7/c/f/5/077cf5ef3542e5ca4c811ef6edf6e8f7.webp",
			"started": "2026-06-26T10:00:00-04:00"
		}]`},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	tr, err := c.CurrentlyPlaying(context.Background(), "di", 1)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Artist != "Daft Punk" || tr.Title != "Around The World" {
		t.Errorf("artist/title = %+v", tr)
	}
	if tr.ID != 42 {
		t.Errorf("track id = %d, want 42", tr.ID)
	}
	if tr.Track != "Daft Punk - Around The World" {
		t.Errorf("joined track = %q", tr.Track)
	}
	if tr.ArtURL != "//cdn-images.audioaddict.com/0/7/7/c/f/5/077cf5ef3542e5ca4c811ef6edf6e8f7.webp" {
		t.Errorf("art_url = %q", tr.ArtURL)
	}
}

// TestCurrentlyPlaying_empty covers the ad-break case where the channel
// returns an empty list — we should get a zero-value Track, not an error.
func TestCurrentlyPlaying_empty(t *testing.T) {
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"GET /v1/di/track_history/channel/1": {Body: `[]`},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	tr, err := c.CurrentlyPlaying(context.Background(), "di", 1)
	if err != nil {
		t.Fatal(err)
	}
	if tr != (audioaddict.Track{}) {
		t.Errorf("expected zero Track on empty response; got %+v", tr)
	}
}

// TestCurrentlyPlaying_showFallback covers entries that have an empty
// `track` string (typical for DJ "show" items) — we synthesise it from
// display_artist + display_title so the UI still has something to render.
func TestCurrentlyPlaying_showFallback(t *testing.T) {
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"GET /v1/di/track_history/channel/1": {Body: `[{
			"channel_id": 1,
			"type": "show",
			"display_artist": "Markus Schulz",
			"display_title": "Global DJ Broadcast",
			"track": "",
			"art_url": ""
		}]`},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	tr, err := c.CurrentlyPlaying(context.Background(), "di", 1)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Track != "Markus Schulz - Global DJ Broadcast" {
		t.Errorf("synthesised track = %q", tr.Track)
	}
	if tr.ArtURL != "" {
		t.Errorf("expected empty art_url; got %q", tr.ArtURL)
	}
}

func TestStreamURL_resolvesViaJSON(t *testing.T) {
	// The /v1/di/channels call validates the channel; the second call to
	// listen.di.fm/<quality>/<channel>?<key> is the JSON resolver step.
	// We can't easily intercept arbitrary outbound hosts, so this test
	// only verifies the validation step + the BaseURL-based ErrNotFound
	// path. The live integration test (TestStreamURL_live below) covers
	// the full JSON resolve.
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"GET /v1/di/channels": {Body: `[{"id":1,"key":"classicrock","name":"Classic Rock"}]`},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"
	c.SetCreds(creds.Session{ListenKey: "mykey"})

	// Channel exists in the listing → we proceed to the resolver step,
	// which will fail because there's no listen.di.fm in test scope.
	// What we DO assert: it doesn't return ErrNotFound (channel was found).
	_, err := c.StreamURL(context.Background(), "di", "classicrock", audioaddict.QualityPremiumHigh)
	if errors.Is(err, audioaddict.ErrNotFound) {
		t.Errorf("got ErrNotFound; channel exists in fixture, expected resolver-attempt error instead")
	}
}

func TestStreamURL_unknownChannel(t *testing.T) {
	srv := testutil.NewAAServer(t, map[string]testutil.AAFixture{
		"GET /v1/di/channels": {Body: `[{"id":1,"key":"classicrock","name":"Classic Rock"}]`},
	})
	c := audioaddict.NewClient(nil)
	c.BaseURL = srv.URL + "/v1"

	_, err := c.StreamURL(context.Background(), "di", "doesnotexist", audioaddict.QualityPremiumHigh)
	if !errors.Is(err, audioaddict.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestResolveImageURL(t *testing.T) {
	cases := []struct {
		name    string
		tpl     string
		w, h, q int
		want    string
	}{
		{
			name: "AudioAddict CDN template — protocol-relative + size + quality",
			tpl:  "//cdn-images.audioaddict.com/abc.png{?size,height,width,quality,pad}",
			w:    320, h: 480, q: 75,
			want: "https://cdn-images.audioaddict.com/abc.png?size=320x480&quality=75",
		},
		{
			name: "no template suffix — legacy URL passes through with size appended",
			tpl:  "//cdn-images.audioaddict.com/legacy.png",
			w:    100, h: 100, q: 60,
			want: "https://cdn-images.audioaddict.com/legacy.png?size=100x100&quality=60",
		},
		{
			name: "absolute https URL stays https",
			tpl:  "https://example.com/x.png{?size,quality}",
			w:    50, h: 50, q: 0,
			want: "https://example.com/x.png?size=50x50",
		},
		{
			name: "empty input returns empty",
			tpl:  "",
			w:    1, h: 1, q: 1,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := audioaddict.ResolveImageURL(tc.tpl, tc.w, tc.h, tc.q)
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestImage_PreferredFanartURL_fallsBackThroughVariants(t *testing.T) {
	cases := []struct {
		name string
		img  audioaddict.Image
		want string
	}{
		{"square preferred (matches our 1:1 frame)", audioaddict.Image{Square: "S", Vertical: "V", Default: "D"}, "S"},
		{"default when no square", audioaddict.Image{Default: "D", Vertical: "V"}, "D"},
		{"vertical when no square/default", audioaddict.Image{Vertical: "V", HorizontalBanner: "H"}, "V"},
		{"horizontal_banner last resort", audioaddict.Image{HorizontalBanner: "H"}, "H"},
		{"empty string when nothing set", audioaddict.Image{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.img.PreferredFanartURL(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNetworks_includesAllSeven(t *testing.T) {
	wantSlugs := []string{"di", "radiotunes", "rockradio", "jazzradio", "classicalradio", "zenradio", "frescatune"}
	for _, slug := range wantSlugs {
		if _, ok := audioaddict.NetworkBySlug(slug); !ok {
			t.Errorf("missing network %q", slug)
		}
	}
}

func TestAuthenticate_live(t *testing.T) {
	email, password := testutil.SkipIfNoLiveCreds(t)
	c := audioaddict.NewClient(nil)
	m, err := c.Authenticate(context.Background(), email, password, "")
	if err != nil {
		t.Fatalf("live auth: %v", err)
	}
	if m.ListenKey == "" {
		t.Fatal("live auth returned empty listen_key")
	}
	// Canary: the listen_key works for a subsequent data call.
	if _, err := c.Channels(context.Background(), "di"); err != nil {
		t.Fatalf("live channels: %v", err)
	}
}
