package ui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/demo"
)

// voteRecorderClient wraps the demo client and records vote calls so the
// tests can assert which API method was invoked. Embeds *demo.FakeClient
// to inherit Channels/StreamURL/CurrentlyPlaying behaviour.
type voteRecorderClient struct {
	*demo.FakeClient
	likeCalls    atomic.Int32
	dislikeCalls atomic.Int32
	unlikeCalls  atomic.Int32
	lastTrackID  atomic.Int64
	lastChanID   atomic.Int64
}

func (v *voteRecorderClient) LikeTrack(_ context.Context, _ string, trackID, channelID int64) error {
	v.likeCalls.Add(1)
	v.lastTrackID.Store(trackID)
	v.lastChanID.Store(channelID)
	return nil
}

func (v *voteRecorderClient) DislikeTrack(_ context.Context, _ string, trackID, channelID int64) error {
	v.dislikeCalls.Add(1)
	v.lastTrackID.Store(trackID)
	v.lastChanID.Store(channelID)
	return nil
}

func (v *voteRecorderClient) UnlikeTrack(_ context.Context, _ string, trackID, channelID int64) error {
	v.unlikeCalls.Add(1)
	v.lastTrackID.Store(trackID)
	v.lastChanID.Store(channelID)
	return nil
}

func newLikeTestModel(t *testing.T) (Model, *voteRecorderClient) {
	t.Helper()
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	fake, _ := demo.NewPlayer(ctx)
	rec := &voteRecorderClient{FakeClient: demo.NewClient()}
	c := demo.Creds()
	c.SessionKey = "test-session"
	m := NewModel(ctx, c, rec,
		func(_ context.Context) (AudioPlayer, error) { return fake, nil })
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
	mm := m2.(Model)
	mm.channels = []audioaddict.Channel{
		{ID: 7, Key: "vocaltrance", Name: "Vocal Trance"},
	}
	mm.player = fake
	mm.currentChannel = "vocaltrance"
	mm.playingNetwork = "di"
	return mm, rec
}

// TestLike_noopWhenNoTrack verifies pressing `l` is silent when there is
// no real track playing (ID == 0 — ad break, show without a track id).
func TestLike_noopWhenNoTrack(t *testing.T) {
	m, rec := newLikeTestModel(t)
	m.currentTrack = audioaddict.Track{ID: 0}

	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	mm := m2.(Model)

	if cmd != nil {
		t.Errorf("expected nil Cmd on `l` with no track; got non-nil")
	}
	if got := rec.likeCalls.Load(); got != 0 {
		t.Errorf("LikeTrack called %d times; want 0", got)
	}
	if len(mm.likedTracks) != 0 {
		t.Errorf("likedTracks = %v; want empty", mm.likedTracks)
	}
}

// TestLike_firstPressLikes verifies pressing `l` on a real track fires
// the LikeTrack Cmd with the right ids and session key, and that the
// voteOKMsg flips likedTracks so the heart renders.
func TestLike_firstPressLikes(t *testing.T) {
	m, rec := newLikeTestModel(t)
	m.currentTrack = audioaddict.Track{ID: 42, Track: "Daft Punk - Around The World"}

	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if cmd == nil {
		t.Fatal("expected non-nil Cmd on `l` with real track")
	}
	mm := m2.(Model)
	if !mm.voteInFlight {
		t.Errorf("voteInFlight = false; want true while Cmd is pending")
	}

	// Execute the Cmd — it should call the vote API and return a voteOKMsg.
	msg := cmd()
	if got := rec.likeCalls.Load(); got != 1 {
		t.Errorf("LikeTrack calls = %d; want 1", got)
	}
	if got := rec.lastTrackID.Load(); got != 42 {
		t.Errorf("LikeTrack trackID = %d; want 42", got)
	}
	if got := rec.lastChanID.Load(); got != 7 {
		t.Errorf("LikeTrack channelID = %d; want 7", got)
	}
	// SessionKey is no longer passed as a string param — the Client owns
	// it internally. We don't assert it here; TestAuthenticate_setsCreds
	// covers the SetCreds path on the audioaddict side.

	ok, isOK := msg.(voteOKMsg)
	if !isOK {
		t.Fatalf("Cmd returned %T; want voteOKMsg", msg)
	}
	if !ok.liked || ok.trackID != 42 {
		t.Errorf("voteOKMsg = %+v; want {trackID:42 liked:true}", ok)
	}

	// Feed the voteOKMsg back into the model and verify the heart renders.
	m3, _ := mm.Update(ok)
	mm = m3.(Model)
	if !mm.likedTracks[42] {
		t.Errorf("likedTracks[42] = false; want true after voteOKMsg")
	}
	if mm.voteInFlight {
		t.Errorf("voteInFlight = true; want false after voteOKMsg")
	}
	if want := Icons().HeartFilled; !strings.Contains(mm.View(), want) {
		t.Errorf("expected HeartFilled glyph %q in view; got:\n%s", want, mm.View())
	}
}

// TestLike_secondPressUnlikes verifies the toggle: a second `l` press on
// an already-liked track dispatches UnlikeTrack.
func TestLike_secondPressUnlikes(t *testing.T) {
	m, rec := newLikeTestModel(t)
	m.currentTrack = audioaddict.Track{ID: 42}
	m.likedTracks = map[int64]bool{42: true}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if cmd == nil {
		t.Fatal("expected non-nil Cmd")
	}
	msg := cmd()
	if got := rec.unlikeCalls.Load(); got != 1 {
		t.Errorf("UnlikeTrack calls = %d; want 1", got)
	}
	if got := rec.likeCalls.Load(); got != 0 {
		t.Errorf("LikeTrack calls = %d; want 0 (second press should unlike)", got)
	}

	ok, isOK := msg.(voteOKMsg)
	if !isOK {
		t.Fatalf("Cmd returned %T; want voteOKMsg", msg)
	}
	if ok.liked || ok.trackID != 42 {
		t.Errorf("voteOKMsg = %+v; want {trackID:42 liked:false}", ok)
	}
}

// TestStatus_helpStringMentionsLike verifies the bottom-bar hints include
// the like keybinding (one of the 4 most-used actions kept after DIMM-394
// trimmed the footer to "top 4 + [?] more").
func TestStatus_helpStringMentionsLike(t *testing.T) {
	m, _ := newLikeTestModel(t)
	if !strings.Contains(m.View(), "[l] like") {
		t.Errorf("expected status hints to include '[l] like'; got:\n%s", m.View())
	}
}

// ---------------------------------------------------------------------------
// DIMM-382: dislike (`s`) toggle
// ---------------------------------------------------------------------------

// TestDislike_noopWhenNoTrack mirrors the like equivalent: `s` is silent
// when there's no real track (ad break, show without a track id).
func TestDislike_noopWhenNoTrack(t *testing.T) {
	m, rec := newLikeTestModel(t)
	m.currentTrack = audioaddict.Track{ID: 0}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd != nil {
		t.Errorf("expected nil Cmd on `s` with no track")
	}
	if got := rec.dislikeCalls.Load(); got != 0 {
		t.Errorf("DislikeTrack called %d times; want 0", got)
	}
}

// TestDislike_firstPressDislikes verifies pressing `s` on a neutral track
// fires DislikeTrack and the resulting voteOKMsg flips dislikedTracks so
// the ⊘ glyph renders.
func TestDislike_firstPressDislikes(t *testing.T) {
	m, rec := newLikeTestModel(t)
	m.currentTrack = audioaddict.Track{ID: 42, Track: "Daft Punk - Around The World"}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd == nil {
		t.Fatal("expected non-nil Cmd")
	}
	msg := cmd()
	if got := rec.dislikeCalls.Load(); got != 1 {
		t.Errorf("DislikeTrack calls = %d; want 1", got)
	}
	if got := rec.likeCalls.Load(); got != 0 {
		t.Errorf("LikeTrack calls = %d; want 0", got)
	}

	ok, isOK := msg.(voteOKMsg)
	if !isOK {
		t.Fatalf("Cmd returned %T; want voteOKMsg", msg)
	}
	if !ok.disliked || ok.liked || ok.trackID != 42 {
		t.Errorf("voteOKMsg = %+v; want {trackID:42 liked:false disliked:true}", ok)
	}

	// Apply the message and confirm the model + view.
	m2, _ := m.Update(ok)
	mm := m2.(Model)
	if !mm.dislikedTracks[42] {
		t.Errorf("dislikedTracks[42] = false; want true")
	}
	if mm.likedTracks[42] {
		t.Errorf("likedTracks[42] = true; want false (mutually exclusive)")
	}
	if want := Icons().HeartBroken; !strings.Contains(mm.View(), want) {
		t.Errorf("expected HeartBroken glyph %q in view; got:\n%s", want, mm.View())
	}
}

// TestDislike_secondPressClears verifies pressing `s` on an already-
// disliked track dispatches UnlikeTrack (DELETE clears either direction).
func TestDislike_secondPressClears(t *testing.T) {
	m, rec := newLikeTestModel(t)
	m.currentTrack = audioaddict.Track{ID: 42}
	m.dislikedTracks = map[int64]bool{42: true}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd == nil {
		t.Fatal("expected non-nil Cmd")
	}
	msg := cmd()
	if got := rec.unlikeCalls.Load(); got != 1 {
		t.Errorf("UnlikeTrack calls = %d; want 1", got)
	}
	if got := rec.dislikeCalls.Load(); got != 0 {
		t.Errorf("DislikeTrack calls = %d; want 0 (second press should clear)", got)
	}

	ok, isOK := msg.(voteOKMsg)
	if !isOK {
		t.Fatalf("Cmd returned %T; want voteOKMsg", msg)
	}
	if ok.liked || ok.disliked || ok.trackID != 42 {
		t.Errorf("voteOKMsg = %+v; want {trackID:42 liked:false disliked:false}", ok)
	}
}

// TestDislike_onLikedClearsLike verifies that pressing `s` on a liked
// track fires DislikeTrack and the resulting voteOKMsg clears the like
// flag (server semantics: POST /down overrides /up).
func TestDislike_onLikedClearsLike(t *testing.T) {
	m, rec := newLikeTestModel(t)
	m.currentTrack = audioaddict.Track{ID: 42}
	m.likedTracks = map[int64]bool{42: true}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd == nil {
		t.Fatal("expected non-nil Cmd")
	}
	msg := cmd()
	if got := rec.dislikeCalls.Load(); got != 1 {
		t.Errorf("DislikeTrack calls = %d; want 1", got)
	}

	mm := m
	m2, _ := mm.Update(msg.(voteOKMsg))
	mm = m2.(Model)
	if mm.likedTracks[42] {
		t.Errorf("likedTracks[42] = true; want false after dislike")
	}
	if !mm.dislikedTracks[42] {
		t.Errorf("dislikedTracks[42] = false; want true")
	}
}

// TestLike_onDislikedClearsDislike is the mirror of the above: pressing
// `l` on a disliked track fires LikeTrack and the resulting voteOKMsg
// clears the dislike flag.
func TestLike_onDislikedClearsDislike(t *testing.T) {
	m, rec := newLikeTestModel(t)
	m.currentTrack = audioaddict.Track{ID: 42}
	m.dislikedTracks = map[int64]bool{42: true}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if cmd == nil {
		t.Fatal("expected non-nil Cmd")
	}
	msg := cmd()
	if got := rec.likeCalls.Load(); got != 1 {
		t.Errorf("LikeTrack calls = %d; want 1", got)
	}

	m2, _ := m.Update(msg.(voteOKMsg))
	mm := m2.(Model)
	if mm.dislikedTracks[42] {
		t.Errorf("dislikedTracks[42] = true; want false after like")
	}
	if !mm.likedTracks[42] {
		t.Errorf("likedTracks[42] = false; want true")
	}
}

// TestStatus_helpStringMentionsDislike verifies `s` is discoverable.
//
// DIMM-394 trimmed the footer to the 4 most-used actions + `[?] more`
// (lazygit/helix convention); `s` (dislike) is one of the less-frequent
// ones and lives in the `?` help overlay (DIMM-392) rather than the
// always-visible footer. Re-spec: discoverability is satisfied if EITHER
// the footer mentions `[s] dislike` OR the footer points at `[?]` for
// the full key list.
func TestStatus_helpStringMentionsDislike(t *testing.T) {
	m, _ := newLikeTestModel(t)
	v := m.View()
	if !strings.Contains(v, "[s] dislike") && !strings.Contains(v, "[?]") {
		t.Errorf("expected status hints to expose dislike either inline ('[s] dislike') or via help pointer ('[?]'); got:\n%s", v)
	}
}

// ---------------------------------------------------------------------------
// Login routing — submitLogin must call AudioClient.Authenticate, not
// fall back to a fresh *audioaddict.Client. Prior code did a type-assert
// against *audioaddict.Client and silently created a NEW one when the
// assertion failed, which meant test/demo clients and any wired Debug
// writer were silently bypassed.
// ---------------------------------------------------------------------------

type loginRecorderClient struct {
	*demo.FakeClient
	authCalls atomic.Int32
	lastEmail atomic.Value // string
}

func (r *loginRecorderClient) Authenticate(_ context.Context, email, _, _ string) (audioaddict.Member, error) {
	r.authCalls.Add(1)
	r.lastEmail.Store(email)
	return audioaddict.Member{
		Email:      email,
		ListenKey:  "rec-lk",
		SessionKey: "rec-sk",
		Premium:    true,
	}, nil
}

func TestLogin_routesAuthThroughInjectedClient(t *testing.T) {
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fake, _ := demo.NewPlayer(ctx)
	rec := &loginRecorderClient{FakeClient: demo.NewClient()}
	m := NewModel(ctx, creds.Session{}, rec,
		func(_ context.Context) (AudioPlayer, error) { return fake, nil })
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
	mm := m2.(Model)
	mm = mm.initLoginInputs(true)
	mm.loginEmail.SetValue("user@example.com")
	mm.loginPassword.SetValue("pw")
	mm.loginField = 2 // [Sign in]

	// Cancel ctx so the LEGACY type-assertion fallback (which would build
	// a fresh *audioaddict.Client and try to hit the real API) returns
	// immediately instead of hanging the test for the full 15s timeout.
	cancel()

	_, cmd := mm.submitLogin()
	if cmd == nil {
		t.Fatal("submitLogin returned nil Cmd")
	}
	_ = cmd() // drain — produces loginSuccessMsg or loginErrorMsg

	if got := rec.authCalls.Load(); got != 1 {
		t.Errorf("Authenticate called on injected client %d times; want 1 (router fell back to a fresh *audioaddict.Client)", got)
	}
	if got, _ := rec.lastEmail.Load().(string); got != "user@example.com" {
		t.Errorf("Authenticate email = %q; want user@example.com", got)
	}
}
