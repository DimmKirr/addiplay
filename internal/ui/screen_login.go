package ui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dimmkirr/addiplay/internal/audioaddict"
	"github.com/dimmkirr/addiplay/internal/creds"
)

// Login overlay messages.
type (
	loginSuccessMsg struct{ creds creds.Creds }
	loginErrorMsg   struct{ err error }
)

// initLoginInputs sets up (or resets) the email/password textinputs and
// flips focus to the login overlay. Called from NewModel (initial state)
// and from auth failures (channels/stream 401).
func (m Model) initLoginInputs(initial bool) Model {
	email := textinput.New()
	email.Placeholder = "you@example.com"
	email.CharLimit = 80
	email.Width = 38
	email.Prompt = ""
	if m.creds.Email != "" {
		email.SetValue(m.creds.Email)
	}
	email.Focus()

	pw := textinput.New()
	pw.Placeholder = "password"
	pw.CharLimit = 128
	pw.Width = 38
	pw.Prompt = ""
	pw.EchoMode = textinput.EchoPassword
	pw.EchoCharacter = '●'

	m.loginEmail = email
	m.loginPassword = pw
	m.loginField = 0
	m.loginBusy = false
	if initial {
		m.loginError = ""
	}
	m.focus = FocusLogin
	return m
}

// updateLogin handles keys while the login overlay is focused.
func (m Model) updateLogin(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Hard exit only when nothing is loaded yet — once a session exists,
	// Esc just closes the overlay and returns to the channel list.
	if msg.Type == tea.KeyCtrlC {
		dlog("login: ctrl-c received")
		// Mirror updateHome's quit path: cancel m.ctx and close the
		// player BEFORE tea.Quit so in-flight HTTPs abort and mpv exits
		// instead of being orphaned. Without this, Ctrl-C from the
		// login overlay leaked goroutines and could leave the terminal
		// in a stale state.
		if m.cancel != nil {
			m.cancel()
			dlog("login: m.cancel() returned")
		}
		if m.player != nil {
			err := m.player.Close()
			dlog("login: m.player.Close() returned (err=%v)", err)
		}
		dlog("login: returning tea.Quit")
		return m, tea.Quit
	}
	if msg.Type == tea.KeyEsc && m.creds.ListenKey != "" {
		m.focus = FocusChannels
		return m, nil
	}

	// While a request is in flight, swallow everything except cancel keys
	// so the user doesn't accidentally fire a second submit.
	if m.loginBusy {
		return m, nil
	}

	switch msg.Type {
	case tea.KeyTab, tea.KeyDown:
		m.loginField = (m.loginField + 1) % 3
		m = m.refocusLogin()
		return m, nil
	case tea.KeyShiftTab, tea.KeyUp:
		m.loginField = (m.loginField + 2) % 3
		m = m.refocusLogin()
		return m, nil
	case tea.KeyEnter:
		// Enter advances from email → password → submit. On submit, fire
		// the auth command.
		if m.loginField < 2 {
			m.loginField++
			m = m.refocusLogin()
			return m, nil
		}
		return m.submitLogin()
	}

	// Route the keystroke to whichever input owns focus.
	var cmd tea.Cmd
	switch m.loginField {
	case 0:
		m.loginEmail, cmd = m.loginEmail.Update(msg)
	case 1:
		m.loginPassword, cmd = m.loginPassword.Update(msg)
	}
	return m, cmd
}

// refocusLogin shifts blink/cursor state so the visually-focused field
// matches m.loginField.
func (m Model) refocusLogin() Model {
	m.loginEmail.Blur()
	m.loginPassword.Blur()
	switch m.loginField {
	case 0:
		m.loginEmail.Focus()
	case 1:
		m.loginPassword.Focus()
	}
	return m
}

// submitLogin fires the auth request and shows a busy indicator. The
// command resolves to loginSuccessMsg (with persisted creds) or
// loginErrorMsg (with a short message).
func (m Model) submitLogin() (tea.Model, tea.Cmd) {
	email := strings.TrimSpace(m.loginEmail.Value())
	password := m.loginPassword.Value()
	if email == "" || password == "" {
		m.loginError = "email and password are required"
		return m, nil
	}
	m.loginBusy = true
	m.loginError = ""
	client := m.client
	parent := m.ctx
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		defer cancel()
		// AudioClient is the UI's narrow interface and doesn't expose
		// Authenticate; we need the concrete *audioaddict.Client for that.
		// Production passes one in via cmd/tui.go, and the demo client
		// embeds the same surface — see if we can type-assert; otherwise
		// fall back to a fresh client (auth is the same against any
		// network's /members/authenticate).
		var aac *audioaddict.Client
		if c, ok := client.(*audioaddict.Client); ok {
			aac = c
		} else {
			aac = audioaddict.NewClient()
		}
		member, err := aac.Authenticate(ctx, email, password)
		if err != nil {
			if errors.Is(err, audioaddict.ErrOAuthOnly) {
				return loginErrorMsg{err: errors.New("this account uses social sign-in; set a password at audioaddict.com/account")}
			}
			if errors.Is(err, audioaddict.ErrAuth) {
				return loginErrorMsg{err: errors.New("invalid email or password")}
			}
			return loginErrorMsg{err: err}
		}
		c := creds.Creds{Email: member.Email, ListenKey: member.ListenKey, Premium: member.Premium}
		if err := creds.Save(c); err != nil {
			return loginErrorMsg{err: err}
		}
		return loginSuccessMsg{creds: c}
	}
}

// viewLogin draws the centered credential form per docs/wireframes/login.svg.
func (m Model) viewLogin() string {
	rowEmailLabel := m.st.muted.Render("Email")
	rowEmail := m.loginEmail.View()
	rowPasswordLabel := m.st.muted.Render("Password")
	rowPassword := m.loginPassword.View()

	submitStyle := m.st.muted.Padding(0, 2)
	if m.loginField == 2 {
		submitStyle = m.st.accentBlock.Padding(0, 2)
	}
	submit := submitStyle.Render("[ Sign in ]")

	status := ""
	switch {
	case m.loginBusy:
		status = m.st.muted.Render("authenticating…")
	case m.loginError != "":
		status = m.st.toast.Render(" " + m.loginError + " ")
	}

	hint := m.st.keyHint.Render("tab next field   enter advance/submit   esc cancel   ctrl-c quit")

	rows := []string{
		m.st.header.Bold(true).Render("Sign in to AudioAddict"),
		m.st.muted.Render(strings.Repeat("─", 56)),
		"",
		m.st.muted.Render("DI.fm / RadioTunes / RockRadio / JazzRadio …"),
		m.st.muted.Render("Premium subscription unlocks 256k streams."),
		"",
		rowEmailLabel,
		rowEmail,
		"",
		rowPasswordLabel,
		rowPassword,
		"",
		submit,
	}
	if status != "" {
		rows = append(rows, "", status)
	}
	rows = append(rows, "", hint)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent).
		Padding(1, 3).
		Render(strings.Join(rows, "\n"))

	return m.st.app.Width(m.width).Height(m.height).Render(
		lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box),
	)
}
