package ui

import (
	"testing"

	"github.com/dimmkirr/addiplay/internal/player"
)

// TestHandleDomain_mediaCommandPlayPause verifies that receiving a
// mediaCommandMsg{PlayPause} while playing calls Pause, and while paused
// calls Resume — the same toggle as the PauseResume key binding.
func TestHandleDomain_mediaCommandPlayPause(t *testing.T) {
	fake := &fakePlayer{state: player.StatePlaying}
	m := Model{player: fake, playerSt: player.StatePlaying}

	m, _ = m.handleDomain(mediaCommandMsg{cmd: player.MediaCommandPlayPause})
	if !fake.paused {
		t.Fatal("expected Pause() to be called when StatePlaying")
	}

	fake.paused = false
	fake.state = player.StatePaused
	m.playerSt = player.StatePaused
	m, _ = m.handleDomain(mediaCommandMsg{cmd: player.MediaCommandPlayPause})
	if !fake.resumed {
		t.Fatal("expected Resume() to be called when StatePaused")
	}
}

// TestHandleDomain_mediaCommandNone verifies that MediaCommandNone is
// ignored (no panic, no player calls).
func TestHandleDomain_mediaCommandNone(t *testing.T) {
	fake := &fakePlayer{state: player.StateIdle}
	m := Model{player: fake, playerSt: player.StateIdle}

	m, _ = m.handleDomain(mediaCommandMsg{cmd: player.MediaCommandNone})
	if fake.paused || fake.resumed {
		t.Fatal("no-op expected for MediaCommandNone")
	}
}

// TestPumpPlayerEventsCmd_mediaCommand verifies that pumpPlayerEventsCmd
// converts Event.MediaCommand into mediaCommandMsg.
func TestPumpPlayerEventsCmd_mediaCommand(t *testing.T) {
	ch := make(chan player.Event, 1)
	ch <- player.Event{MediaCommand: player.MediaCommandNext}

	fake := &fakePlayer{eventsCh: ch}
	cmd := pumpPlayerEventsCmd(fake)
	if cmd == nil {
		t.Fatal("expected non-nil Cmd")
	}
	msg := cmd()
	mc, ok := msg.(mediaCommandMsg)
	if !ok {
		t.Fatalf("expected mediaCommandMsg; got %T", msg)
	}
	if mc.cmd != player.MediaCommandNext {
		t.Errorf("cmd = %v, want Next", mc.cmd)
	}
}

// fakePlayer is a minimal AudioPlayer used only in this file.
type fakePlayer struct {
	state    player.State
	paused   bool
	resumed  bool
	eventsCh chan player.Event
}

func (f *fakePlayer) Play(string) error                   { return nil }
func (f *fakePlayer) Pause() error                        { f.paused = true; return nil }
func (f *fakePlayer) Resume() error                       { f.resumed = true; return nil }
func (f *fakePlayer) Stop() error                         { return nil }
func (f *fakePlayer) SetVolume(int) error                 { return nil }
func (f *fakePlayer) SetTrackMetadata(string, string, int, string) error { return nil }
func (f *fakePlayer) Close() error                        { return nil }
func (f *fakePlayer) Events() <-chan player.Event          { return f.eventsCh }
func (f *fakePlayer) State() player.State                  { return f.state }
