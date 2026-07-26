package player_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dimmkirr/addiplay/internal/nowplaying"
	"github.com/dimmkirr/addiplay/internal/player"
	"github.com/dimmkirr/addiplay/internal/testutil"
)

func TestPlayer_sendsLoadfileToMPV(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	p, err := player.NewWithSocket(ctx, mpv.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Play("http://example.com/stream"); err != nil {
		t.Fatal(err)
	}
	// 3 observe_property (startup: pause + media-title + metadata) + loadfile + set_property pause false
	mpv.WaitCommand(t, 5)

	cmds := mpv.Commands()
	// Skip the three observe_property commands sent at startup.
	load, _ := cmds[3]["command"].([]any)
	if len(load) < 2 || load[0] != "loadfile" || load[1] != "http://example.com/stream" {
		t.Errorf("loadfile command = %v, want loadfile <url>", load)
	}
}

func TestPlayer_pauseResumeStop(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	p, err := player.NewWithSocket(ctx, mpv.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	_ = p.Play("http://x/y")
	_ = p.Pause()
	_ = p.Resume()
	_ = p.Stop()
	// 3 observe_property + loadfile + unpause + pause + resume + stop = 8
	mpv.WaitCommand(t, 8)

	cmds := mpv.Commands()
	// Skip 3 observe_property at indices 0-2.
	wantHeads := []string{"loadfile", "set_property", "set_property", "set_property", "stop"}
	for i, want := range wantHeads {
		got, _ := cmds[i+3]["command"].([]any)
		if len(got) == 0 || got[0] != want {
			t.Errorf("cmd[%d] head = %v, want %q", i, got, want)
		}
	}
}

func TestPlayer_Stop_setsNowPlayingPaused(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var buf bytes.Buffer
	np := nowplaying.NewLog(&buf)

	p, err := player.NewWithSocket(ctx, mpv.SocketPath, player.WithNowPlaying(np))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	_ = p.Play("http://example.com/stream")
	mpv.WaitCommand(t, 5) // 3 observe + loadfile + unpause
	buf.Reset()

	_ = p.Stop()
	mpv.WaitCommand(t, 6) // +stop

	got := buf.String()
	if !strings.Contains(got, "playing=false") {
		t.Errorf("expected SetPlaying(false) on Stop, got: %s", got)
	}
}

func TestPlayer_setVolumeClamps(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	p, err := player.NewWithSocket(ctx, mpv.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	_ = p.SetVolume(-10)
	_ = p.SetVolume(150)
	// 3 observe_property + 2 set_property volume = 5
	mpv.WaitCommand(t, 5)

	cmds := mpv.Commands()
	// Skip 3 observe_property at indices 0-2.
	c1, _ := cmds[3]["command"].([]any)
	c2, _ := cmds[4]["command"].([]any)
	if got := c1[len(c1)-1]; got != float64(0) {
		t.Errorf("volume clamp low: got %v, want 0", got)
	}
	if got := c2[len(c2)-1]; got != float64(100) {
		t.Errorf("volume clamp high: got %v, want 100", got)
	}
}

func TestPlayer_SetTrackMetadata_routesThroughNowPlaying(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var buf bytes.Buffer
	np := nowplaying.NewLog(&buf)

	p, err := player.NewWithSocket(ctx, mpv.SocketPath, player.WithNowPlaying(np))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.SetTrackMetadata("The Police", "Don't Stand So Close To Me", 240, ""); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	if !strings.Contains(got, `artist="The Police"`) {
		t.Errorf("expected nowplaying.Update call with artist, got: %s", got)
	}
	if !strings.Contains(got, `title="Don't Stand So Close To Me"`) {
		t.Errorf("expected nowplaying.Update call with title, got: %s", got)
	}
}

func TestPlayer_Close_closesNowPlaying(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var buf bytes.Buffer
	np := nowplaying.NewLog(&buf)

	p, err := player.NewWithSocket(ctx, mpv.SocketPath, player.WithNowPlaying(np))
	if err != nil {
		t.Fatal(err)
	}

	p.Close()

	got := buf.String()
	if !strings.Contains(got, "[nowplaying] Close") {
		t.Errorf("expected nowplaying.Close call, got: %s", got)
	}
}

func TestPlayer_Play_setsNowPlayingPlaying(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var buf bytes.Buffer
	np := nowplaying.NewLog(&buf)

	p, err := player.NewWithSocket(ctx, mpv.SocketPath, player.WithNowPlaying(np))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	_ = p.Play("http://example.com/stream")
	mpv.WaitCommand(t, 5) // 3 observe + loadfile + unpause

	got := buf.String()
	if !strings.Contains(got, "playing=true") {
		t.Errorf("expected SetPlaying(true) on Play, got: %s", got)
	}
}

func TestPlayer_Pause_setsNowPlayingState(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var buf bytes.Buffer
	np := nowplaying.NewLog(&buf)

	p, err := player.NewWithSocket(ctx, mpv.SocketPath, player.WithNowPlaying(np))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	_ = p.Play("http://example.com/stream")
	mpv.WaitCommand(t, 5) // 3 observe + loadfile + unpause
	buf.Reset()

	_ = p.Pause()
	mpv.WaitCommand(t, 6) // +pause

	got := buf.String()
	if !strings.Contains(got, "playing=false") {
		t.Errorf("expected SetPlaying(false) on Pause, got: %s", got)
	}
}

func TestPlayer_Resume_setsNowPlayingState(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var buf bytes.Buffer
	np := nowplaying.NewLog(&buf)

	p, err := player.NewWithSocket(ctx, mpv.SocketPath, player.WithNowPlaying(np))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	_ = p.Resume()
	mpv.WaitCommand(t, 4) // 3 observe + resume

	got := buf.String()
	if !strings.Contains(got, "playing=true") {
		t.Errorf("expected SetPlaying(true) on Resume, got: %s", got)
	}
}

func TestMediaCommand_String(t *testing.T) {
	tests := []struct {
		cmd  player.MediaCommand
		want string
	}{
		{player.MediaCommandNone, "None"},
		{player.MediaCommandPlayPause, "PlayPause"},
		{player.MediaCommandNext, "Next"},
		{player.MediaCommandPrevious, "Previous"},
	}
	for _, tt := range tests {
		if got := tt.cmd.String(); got != tt.want {
			t.Errorf("MediaCommand(%d).String() = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestPlayer_InjectMediaCommand(t *testing.T) {
	mpv := testutil.NewMPVFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	p, err := player.NewWithSocket(ctx, mpv.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	p.InjectMediaCommand(player.MediaCommandPlayPause)

	select {
	case ev := <-p.Events():
		if ev.MediaCommand != player.MediaCommandPlayPause {
			t.Errorf("got MediaCommand=%v, want PlayPause", ev.MediaCommand)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for injected media command event")
	}
}

func TestState_String(t *testing.T) {
	cases := map[player.State]string{
		player.StateIdle: "idle", player.StateLoading: "loading",
		player.StatePlaying: "playing", player.StatePaused: "paused",
		player.StateError: "error",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", s, got, want)
		}
	}
}
