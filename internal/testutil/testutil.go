// Package testutil holds shared test helpers: teatest wrappers, golden-file
// snapshots, an httptest server that fakes the AudioAddict API, and a fake
// mpv JSON-IPC socket. Domain packages import this to write tests; nothing
// in production code may import it.
package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/google/go-cmp/cmp"
)

// goldenUpdate returns whether tests should rewrite golden files. It reuses
// the `-update` flag registered by charmbracelet/x/exp/golden (transitively
// imported via teatest) so we don't redefine the same flag and panic.
func goldenUpdate() bool {
	if f := flag.Lookup("update"); f != nil {
		return f.Value.String() == "true"
	}
	return false
}

// -----------------------------------------------------------------------------
// Live-credential gate
// -----------------------------------------------------------------------------

// SkipIfNoLiveCreds skips the test when AA_INTEGRATION_EMAIL or
// AA_INTEGRATION_PASSWORD are unset. Use it at the top of every integration
// test that hits the real AudioAddict API.
func SkipIfNoLiveCreds(t *testing.T) (email, password string) {
	t.Helper()
	email = os.Getenv("AA_INTEGRATION_EMAIL")
	password = os.Getenv("AA_INTEGRATION_PASSWORD")
	if email == "" || password == "" {
		t.Skip("integration test skipped: set AA_INTEGRATION_EMAIL and AA_INTEGRATION_PASSWORD (see DIMM-365)")
	}
	return email, password
}

// -----------------------------------------------------------------------------
// Golden files
// -----------------------------------------------------------------------------

// AssertGolden compares `got` against testdata/golden/<name>.txt next to the
// calling test file. Pass `-update` to rewrite the golden.
func AssertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if goldenUpdate() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go test -update` to create)", path, err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("golden mismatch (-want +got):\n%s", diff)
	}
}

// -----------------------------------------------------------------------------
// teatest wrappers
// -----------------------------------------------------------------------------

// NewModel wraps teatest.NewTestModel with sensible defaults.
func NewModel(t *testing.T, m tea.Model) *teatest.TestModel {
	t.Helper()
	return teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
}

// SendKey sends a single key to the model.
func SendKey(_ *testing.T, tm *teatest.TestModel, r rune) {
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

// WaitFor waits up to 5s for the model's output to satisfy predicate.
func WaitFor(t *testing.T, tm *teatest.TestModel, predicate func([]byte) bool) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), predicate, teatest.WithDuration(5*time.Second))
}

// FinalFrame quits the program and returns the last rendered frame.
func FinalFrame(t *testing.T, tm *teatest.TestModel) string {
	t.Helper()
	tm.Send(tea.QuitMsg{})
	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	out := tm.FinalOutput(t, teatest.WithFinalTimeout(2*time.Second))
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(out); err != nil {
		t.Fatalf("read final output: %v", err)
	}
	return buf.String()
}

// -----------------------------------------------------------------------------
// AudioAddict httptest fake
// -----------------------------------------------------------------------------

// AAFixture maps "method path" to a response body (raw JSON) and status.
type AAFixture struct {
	Status int
	Body   string
}

// NewAAServer returns an httptest.Server pre-loaded with the given fixtures.
// Missing routes return 404. Caller MUST call srv.Close() (use t.Cleanup).
func NewAAServer(t *testing.T, routes map[string]AAFixture) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for k, v := range routes {
		method, path, ok := strings.Cut(k, " ")
		if !ok {
			t.Fatalf("invalid route key %q (want \"GET /path\")", k)
		}
		f := v
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if f.Status != 0 {
				w.WriteHeader(f.Status)
			}
			_, _ = fmt.Fprint(w, f.Body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// MustJSON marshals v or fails the test.
func MustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// -----------------------------------------------------------------------------
// mpv JSON-IPC fake socket
// -----------------------------------------------------------------------------

// MPVFake is a Unix socket that speaks the mpv JSON-IPC protocol enough to
// satisfy internal/player tests. It accepts commands and replies with the
// canned events in Events on each Recv from the player.
type MPVFake struct {
	SocketPath string
	mu         sync.Mutex
	commands   []map[string]any
	done       chan struct{}
	ln         net.Listener
}

// NewMPVFake starts a listening socket and returns the path. The socket is
// closed on test cleanup.
func NewMPVFake(t *testing.T) *MPVFake {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mpv.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix %s: %v", path, err)
	}
	f := &MPVFake{SocketPath: path, done: make(chan struct{}), ln: ln}
	go f.accept(t)
	t.Cleanup(func() { _ = ln.Close(); close(f.done) })
	return f
}

func (f *MPVFake) accept(t *testing.T) {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.serve(t, conn)
	}
}

func (f *MPVFake) serve(_ *testing.T, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var cmd map[string]any
		if err := dec.Decode(&cmd); err != nil {
			return
		}
		f.mu.Lock()
		f.commands = append(f.commands, cmd)
		f.mu.Unlock()
		// Reply with success ack like real mpv does.
		ack := map[string]any{"error": "success", "request_id": cmd["request_id"]}
		if err := enc.Encode(ack); err != nil {
			return
		}
	}
}

// Commands returns the slice of commands received so far.
func (f *MPVFake) Commands() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.commands))
	copy(out, f.commands)
	return out
}

// WaitCommand blocks until at least n commands have been received, or 2s elapses.
func (f *MPVFake) WaitCommand(t *testing.T, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		if len(f.Commands()) >= n {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for mpv command %d; got %d", n, len(f.Commands()))
		case <-time.After(10 * time.Millisecond):
		}
	}
}
