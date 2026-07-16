package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
	"github.com/dimmkirr/addiplay/internal/demo"
)

// --- pendingSkip replay tests (DIMM-423) ---

// TestSkipErr_sessionInvalid_stashesPendingSkip verifies that a skip failure
// with sessionInvalid=true stashes the skip context and pops the login overlay,
// mirroring the existing pendingVote behaviour.
func TestSkipErr_sessionInvalid_stashesPendingSkip(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.playingNetwork = "di"
	m.currentTrack = audioaddict.Track{ID: 42, Duration: 300}
	m.creds.SessionKey = "old-session"
	m.skipInFlight = true

	ch := audioaddict.Channel{ID: 7, Key: "vocaltrance", Name: "Vocal Trance"}

	m2, _ := m.Update(skipErrMsg{
		err:            audioaddict.ErrSessionInvalid,
		sessionInvalid: true,
		network:        "di",
		trackID:        42,
		channelID:      7,
		channel:        ch,
	})
	mm := m2.(Model)

	if mm.pendingSkip == nil {
		t.Fatal("expected pendingSkip to be stashed after sessionInvalid skip error")
	}
	if mm.pendingSkip.trackID != 42 {
		t.Errorf("pendingSkip.trackID = %d; want 42", mm.pendingSkip.trackID)
	}
	if mm.pendingSkip.channelID != 7 {
		t.Errorf("pendingSkip.channelID = %d; want 7", mm.pendingSkip.channelID)
	}
	if mm.pendingSkip.network != "di" {
		t.Errorf("pendingSkip.network = %q; want di", mm.pendingSkip.network)
	}
	if mm.focus != FocusLogin {
		t.Errorf("focus = %d; want FocusLogin (%d)", mm.focus, FocusLogin)
	}
	if mm.skipInFlight {
		t.Error("skipInFlight should be cleared after skip error")
	}
}

// TestSkipErr_nonSession_noPendingSkip verifies that non-session skip errors
// do NOT stash a pendingSkip — only sessionInvalid triggers the replay path.
func TestSkipErr_nonSession_noPendingSkip(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.playingNetwork = "di"
	m.skipInFlight = true

	m2, _ := m.Update(skipErrMsg{
		err:            audioaddict.ErrNotFound,
		sessionInvalid: false,
	})
	mm := m2.(Model)

	if mm.pendingSkip != nil {
		t.Error("pendingSkip should NOT be stashed for non-session errors")
	}
	if mm.focus == FocusLogin {
		t.Error("login overlay should NOT pop for non-session skip errors")
	}
}

// TestLogin_replaysPendingSkip verifies that after re-auth, a stashed
// pendingSkip is replayed (a skipTrackCmd is dispatched) and cleared.
func TestLogin_replaysPendingSkip(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.playingNetwork = "di"
	m.currentTrack = audioaddict.Track{ID: 42, Duration: 300}
	ch := audioaddict.Channel{ID: 7, Key: "vocaltrance", Name: "Vocal Trance"}
	m.channels = []audioaddict.Channel{ch}
	m.player, _ = demo.NewPlayer(context.Background())

	m.pendingSkip = &pendingSkip{
		network:   "di",
		trackID:   42,
		channelID: 7,
		channel:   ch,
	}

	m2, cmd := m.Update(loginSuccessMsg{creds: creds.Session{
		Email:      "test@example.com",
		ListenKey:  "lk",
		SessionKey: "new-sk",
		AudioToken: "new-at",
		Premium:    true,
	}})
	mm := m2.(Model)

	if mm.pendingSkip != nil {
		t.Error("pendingSkip should be cleared after login replay")
	}
	if !mm.skipInFlight {
		t.Error("skipInFlight should be true after replaying pendingSkip")
	}
	if cmd == nil {
		t.Error("expected a Cmd to be returned for skip replay")
	}
}

// TestLogin_replaysPendingSkip_clearedEvenIfBothPending verifies that both
// pendingVote and pendingSkip are replayed on loginSuccessMsg.
func TestLogin_replaysPendingSkip_clearedEvenIfBothPending(t *testing.T) {
	m := newTestModel(t)
	m.currentChannel = "vocaltrance"
	m.playingNetwork = "di"
	m.currentTrack = audioaddict.Track{ID: 42, Duration: 300}
	ch := audioaddict.Channel{ID: 7, Key: "vocaltrance", Name: "Vocal Trance"}
	m.channels = []audioaddict.Channel{ch}
	m.player, _ = demo.NewPlayer(context.Background())

	m.pendingVote = &pendingVote{
		network:   "di",
		trackID:   42,
		channelID: 7,
		dir:       voteUp,
	}
	m.pendingSkip = &pendingSkip{
		network:   "di",
		trackID:   42,
		channelID: 7,
		channel:   ch,
	}

	m2, cmd := m.Update(loginSuccessMsg{creds: creds.Session{
		Email:      "test@example.com",
		ListenKey:  "lk",
		SessionKey: "new-sk",
		AudioToken: "new-at",
		Premium:    true,
	}})
	mm := m2.(Model)

	if mm.pendingVote != nil {
		t.Error("pendingVote should be cleared")
	}
	if mm.pendingSkip != nil {
		t.Error("pendingSkip should be cleared")
	}
	if cmd == nil {
		t.Error("expected Cmd(s) for both replay operations")
	}
}

// --- audio_token expiry toast tests (DIMM-423) ---

// TestStreamPlaying_emptyAudioToken_showsToast verifies that when per-track
// mode degrades to live-stream because audio_token is empty, the user sees a
// toast explaining the degradation instead of silent fallback.
func TestStreamPlaying_emptyAudioToken_showsToast(t *testing.T) {
	m := newTestModel(t)
	m.creds.AudioToken = ""
	m.currentNetwork = "di"
	ch := audioaddict.Channel{ID: 7, Key: "vocaltrance", Name: "Vocal Trance"}

	m2, _ := m.Update(streamPlayingMsg{
		network: "di",
		channel: ch,
	})
	mm := m2.(Model)

	if mm.toast == "" {
		t.Fatal("expected toast when audio_token is empty on stream start")
	}
	if !strings.Contains(mm.toast, "sign in") {
		t.Errorf("toast should mention re-login; got %q", mm.toast)
	}
}
