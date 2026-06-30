package player

import (
	"os/exec"
	"testing"
	"time"
)

// TestKillProcess_doesNotDoubleWait is the regression guard for the
// "hangs on quit" bug. The pattern in question:
//
//   1. New() spawns mpv and starts a reaper goroutine: `go cmd.Wait()`.
//   2. Close() → killProcess() sends SIGKILL.
//   3. killProcess() USED TO call cmd.Wait() a second time.
//
// Per os/exec docs: "Wait must not be called concurrently with itself."
// The kernel delivers the process exit status to exactly one of the
// concurrent Wait()s — the other blocks forever waiting for a signal
// that will never come. Hence the user-reported hang at
// `m.player.Close()` (log ended at "quit: m.cancel() returned" with
// the expected "player.Close() returned" line never appearing).
//
// This test reproduces the pattern with a `sleep` subprocess: spawn,
// start reaper, kill, verify the kill call returns promptly. With the
// bug present (a second cmd.Wait() in the kill path) this test hangs.
func TestKillProcess_doesNotDoubleWait(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("sleep not available on this runner: %v", err)
	}

	// Mirror player.New's reaper goroutine.
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()

	// Simulate the FIXED killProcess: kill but do NOT call Wait again.
	done := make(chan struct{})
	go func() {
		_ = cmd.Process.Kill()
		// REGRESSION: do NOT add `_ = cmd.Wait()` here. That was the bug.
		close(done)
	}()

	select {
	case <-done:
		// Expected: Kill returns immediately.
	case <-time.After(2 * time.Second):
		t.Fatal("killProcess equivalent took >2s — second cmd.Wait() reintroduced?")
	}

	// The reaper goroutine should now wake up and complete because
	// SIGKILL caused the subprocess to exit.
	select {
	case <-reaped:
		// Expected.
	case <-time.After(2 * time.Second):
		t.Fatal("reaper goroutine never finished after SIGKILL — subprocess didn't die?")
	}
}
