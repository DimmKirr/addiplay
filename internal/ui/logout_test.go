package ui

import (
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/demo"
)

// logoutSpyClient counts Logout calls so the test can prove the first
// `L` press does NOT actually log out.
type logoutSpyClient struct {
	*demo.FakeClient
	logoutCalls atomic.Int32
}

func (s *logoutSpyClient) Logout() error {
	s.logoutCalls.Add(1)
	return s.FakeClient.Logout()
}

func newLogoutTestModel(t *testing.T) (Model, *logoutSpyClient) {
	t.Helper()
	m := newTestModel(t)
	spy := &logoutSpyClient{FakeClient: demo.NewClient()}
	m.client = spy
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return sized.(Model), spy
}

// TestLogout_firstPressRequiresConfirmation enforces DIMM-393: pressing
// `L` once must NOT call `m.client.Logout()`. It must arm a confirmation
// (pendingLogout + toast) and wait for a second press. `L` sits one
// shift-key away from `l` (like), so a single touch-typed shift-graze
// shouldn't wipe the session.
func TestLogout_firstPressRequiresConfirmation(t *testing.T) {
	m, spy := newLogoutTestModel(t)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	got := updated.(Model)

	if spy.logoutCalls.Load() != 0 {
		t.Errorf("first `L` press triggered Logout() %d times; want 0", spy.logoutCalls.Load())
	}
	if !got.pendingLogout {
		t.Errorf("pendingLogout = false after first `L`; want true")
	}
	if !strings.Contains(got.toast, "logout") {
		t.Errorf("toast should warn about pending logout; got: %q", got.toast)
	}
	if got.focus == FocusLogin {
		t.Errorf("focus shouldn't switch to FocusLogin until confirmation; got focus=%d", got.focus)
	}
}

// TestLogout_secondPressCompletesLogout — confirm the confirmation path.
func TestLogout_secondPressCompletesLogout(t *testing.T) {
	m, spy := newLogoutTestModel(t)

	once, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	twice, _ := once.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	got := twice.(Model)

	if spy.logoutCalls.Load() != 1 {
		t.Errorf("two `L` presses triggered Logout() %d times; want 1", spy.logoutCalls.Load())
	}
	if got.focus != FocusLogin {
		t.Errorf("after confirmed logout, focus = %d; want FocusLogin", got.focus)
	}
}

// TestLogout_escCancelsPending — pressing esc clears the pending state
// so a future `L` press requires its own confirmation again.
func TestLogout_escCancelsPending(t *testing.T) {
	m, _ := newLogoutTestModel(t)

	armed, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	cleared, _ := armed.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := cleared.(Model)

	if got.pendingLogout {
		t.Errorf("after esc, pendingLogout still true; want false")
	}
}
