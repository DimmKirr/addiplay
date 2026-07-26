package nowplaying_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dimmkirr/addiplay/internal/nowplaying"
)

func TestLogProvider_Update(t *testing.T) {
	var buf bytes.Buffer
	p := nowplaying.NewLog(&buf)

	p.Update("The Police", "Don't Stand So Close To Me", 240, "")

	got := buf.String()
	if !strings.Contains(got, `artist="The Police"`) {
		t.Errorf("expected artist in log, got: %s", got)
	}
	if !strings.Contains(got, `title="Don't Stand So Close To Me"`) {
		t.Errorf("expected title in log, got: %s", got)
	}
}

func TestLogProvider_SetPlaying(t *testing.T) {
	var buf bytes.Buffer
	p := nowplaying.NewLog(&buf)

	p.SetPlaying(true)
	p.SetPlaying(false)

	got := buf.String()
	if !strings.Contains(got, "playing=true") {
		t.Errorf("expected playing=true in log, got: %s", got)
	}
	if !strings.Contains(got, "playing=false") {
		t.Errorf("expected playing=false in log, got: %s", got)
	}
}

func TestLogProvider_CloseIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	p := nowplaying.NewLog(&buf)

	p.Close()
	p.Close() // must not panic

	got := buf.String()
	if strings.Count(got, "[nowplaying] Close") != 1 {
		t.Errorf("expected exactly one Close log line, got: %s", got)
	}
}

func TestLogProvider_IgnoresAfterClose(t *testing.T) {
	var buf bytes.Buffer
	p := nowplaying.NewLog(&buf)

	p.Close()
	buf.Reset()

	p.Update("ignored", "ignored", 0, "")
	p.SetPlaying(true)

	if buf.Len() > 0 {
		t.Errorf("expected no output after Close, got: %s", buf.String())
	}
}

func TestNew_ReturnsProvider(t *testing.T) {
	var buf bytes.Buffer
	var received []nowplaying.Command

	handler := func(cmd nowplaying.Command) {
		received = append(received, cmd)
	}

	p := nowplaying.New(handler, nowplaying.WithLogWriter(&buf))
	if p == nil {
		t.Fatal("New returned nil")
	}

	p.Update("Kool & The Gang", "Cherish", 180, "https://example.com/art.jpg")
	p.SetPlaying(true)
	p.Close()

	got := buf.String()
	if !strings.Contains(got, "Kool & The Gang") {
		t.Errorf("expected artist in log, got: %s", got)
	}
}

func TestCommand_String(t *testing.T) {
	tests := []struct {
		cmd  nowplaying.Command
		want string
	}{
		{nowplaying.CmdPlayPause, "PlayPause"},
		{nowplaying.CmdNext, "Next"},
		{nowplaying.CmdPrevious, "Previous"},
	}
	for _, tt := range tests {
		if got := tt.cmd.String(); got != tt.want {
			t.Errorf("Command(%d).String() = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}
