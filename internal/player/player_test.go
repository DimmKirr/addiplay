package player_test

import (
	"context"
	"testing"
	"time"

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
	// observe_property (startup) + loadfile + set_property pause false
	mpv.WaitCommand(t, 3)

	cmds := mpv.Commands()
	// Skip the observe_property command sent at startup.
	load, _ := cmds[1]["command"].([]any)
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
	// observe_property + loadfile + unpause + pause + resume + stop = 6
	mpv.WaitCommand(t, 6)

	cmds := mpv.Commands()
	// Skip observe_property at index 0.
	wantHeads := []string{"loadfile", "set_property", "set_property", "set_property", "stop"}
	for i, want := range wantHeads {
		got, _ := cmds[i+1]["command"].([]any)
		if len(got) == 0 || got[0] != want {
			t.Errorf("cmd[%d] head = %v, want %q", i, got, want)
		}
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
	// observe_property + 2 set_property volume = 3
	mpv.WaitCommand(t, 3)

	cmds := mpv.Commands()
	// Skip observe_property at index 0.
	c1, _ := cmds[1]["command"].([]any)
	c2, _ := cmds[2]["command"].([]any)
	if got := c1[len(c1)-1]; got != float64(0) {
		t.Errorf("volume clamp low: got %v, want 0", got)
	}
	if got := c2[len(c2)-1]; got != float64(100) {
		t.Errorf("volume clamp high: got %v, want 100", got)
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
