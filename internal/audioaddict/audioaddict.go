// Package audioaddict is a small HTTP client for the (undocumented)
// AudioAddict member API used by DI.fm, RadioTunes, RockRadio, JazzRadio,
// ClassicalRadio, ZenRadio and FrescaTune.
//
// The API surface was cross-checked against three reference clients:
//   - github.com/GeertJohan/tune          (Go)
//   - github.com/DannyBen/audio_addict    (Ruby)
//   - github.com/nilicule/mopidy-audioaddict (Python)
//
// Endpoint base: https://api.audioaddict.com/v1/<network>
package audioaddict

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dimmkirr/addiplay/internal/creds"
)

// dlogf writes a single log line to w (no-op when w is nil) prefixed with
// an RFC3339Nano timestamp and the caller's file:line. Used by the auth
// diagnostics so debug.log lines are self-locating without having to grep
// for the bare `[audioaddict]` prefix.
func dlogf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	_, file, line, ok := runtime.Caller(1)
	caller := "?"
	if ok {
		caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
	}
	_, _ = fmt.Fprintf(w, "[%s] [audioaddict] [%s] ", ts, caller)
	_, _ = fmt.Fprintf(w, format, args...)
	if !strings.HasSuffix(format, "\n") {
		_, _ = fmt.Fprintln(w)
	}
}

// maskJSONSecrets walks a decoded JSON value and replaces any string value
// stored under a sensitive key (anything matching listen_key, session_key,
// password, token, auth, secret, key=…) with `<redacted:len=N>`. Other
// string values are kept as-is so we can see the actual response shape
// (top-level field names, member field names, user_type, email, etc.)
// without leaking the streaming/session tokens themselves.
func maskJSONSecrets(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if isSensitiveKey(k) {
				if s, ok := val.(string); ok {
					out[k] = fmt.Sprintf("<redacted:len=%d>", len(s))
					continue
				}
			}
			out[k] = maskJSONSecrets(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = maskJSONSecrets(val)
		}
		return out
	default:
		return v
	}
}

// isSensitiveKey decides whether a JSON field's value should be redacted
// in debug logs. Matches the common token-bearing field name patterns.
func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, needle := range []string{"key", "token", "password", "secret", "auth"} {
		if strings.Contains(lk, needle) {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

// Public errors. Callers may errors.Is() against these.
var (
	// ErrAuth — login endpoint rejected the supplied email/password.
	ErrAuth = errors.New("audioaddict: invalid credentials")
	// ErrOAuthOnly — account has no password (Google/Facebook sign-up).
	ErrOAuthOnly = errors.New("audioaddict: account uses social login (no password)")
	// ErrUnauthorized — generic 401/403 from any data endpoint. Kept as
	// the parent of the two more specific errors below so existing
	// `errors.Is(err, ErrUnauthorized)` callers keep working.
	ErrUnauthorized = errors.New("audioaddict: unauthorized")
	// ErrSessionInvalid — 401/403 on a write endpoint (vote, favorite).
	// The X-Session-Key has been rejected; UI should pop the login overlay
	// and replay the pending op after re-auth. Stream playback is
	// unaffected because it uses listen_key, not session_key.
	ErrSessionInvalid = fmt.Errorf("audioaddict: session_key rejected: %w", ErrUnauthorized)
	// ErrListenKeyRejected — 401/403 on the stream-URL resolver (listen.<net>).
	// The user's listen_key is dead — full re-login required, the streaming
	// surface is broken until they sign in again.
	ErrListenKeyRejected = fmt.Errorf("audioaddict: listen_key rejected: %w", ErrUnauthorized)
	// ErrNotFound — 404 from a data endpoint.
	ErrNotFound = errors.New("audioaddict: not found")
	// ErrRateLimit — 429 from the API.
	ErrRateLimit = errors.New("audioaddict: rate limited")
)

// -----------------------------------------------------------------------------
// Networks
// -----------------------------------------------------------------------------

// Network describes a single AudioAddict-family service.
type Network struct {
	Slug    string // URL slug, e.g. "di", "rockradio"
	Display string // human-facing brand name
	Site    string // public website
}

// Networks lists every network this client knows. The slug is what the
// AudioAddict API expects in the URL path.
var Networks = []Network{
	{Slug: "di", Display: "DI.fm", Site: "https://www.di.fm/"},
	{Slug: "radiotunes", Display: "RadioTunes", Site: "https://www.radiotunes.com/"},
	{Slug: "rockradio", Display: "RockRadio", Site: "https://www.rockradio.com/"},
	{Slug: "jazzradio", Display: "JazzRadio", Site: "https://www.jazzradio.com/"},
	{Slug: "classicalradio", Display: "ClassicalRadio", Site: "https://www.classicalradio.com/"},
	{Slug: "zenradio", Display: "ZenRadio", Site: "https://www.zenradio.com/"},
	{Slug: "frescatune", Display: "FrescaTune", Site: "https://www.frescatune.com/"},
}

// NetworkBySlug returns the Network with the given slug, or false.
func NetworkBySlug(slug string) (Network, bool) {
	for _, n := range Networks {
		if n.Slug == slug {
			return n, true
		}
	}
	return Network{}, false
}

// -----------------------------------------------------------------------------
// Quality tiers
// -----------------------------------------------------------------------------

// Quality is a stream quality preset; the listen_key gates access.
type Quality string

const (
	// QualityPublic — free / ad-supported public stream (no listen_key needed).
	QualityPublic Quality = "public3"
	// QualityPremium — 128 kbps mp3 (premium account).
	QualityPremium Quality = "premium"
	// QualityPremiumHigh — 256 kbps mp3 (premium account).
	QualityPremiumHigh Quality = "premium_high"
	// QualityPremiumMedium — 64 kbps aac (premium account, lower bandwidth).
	QualityPremiumMedium Quality = "premium_medium"
)

// -----------------------------------------------------------------------------
// Client
// -----------------------------------------------------------------------------

// Storage is the persistence surface the Client uses to keep auth state
// across launches. It's a duck-typed interface so `creds.Storage` (which
// already implements Save/Load/Clear over the keyring + chmod-600 JSON
// file) can be passed in without `creds` having to import this package.
//
// Tests can inject an in-memory or no-op implementation. nil is allowed —
// methods that would persist just skip the side effect.
type Storage interface {
	Save(s creds.Session) error
	Load() (creds.Session, error)
	Clear() error
}

// defaultHeadersTransport sets the credentials and metadata that ALL
// AudioAddict API requests need, so individual call sites never have to
// remember them:
//
//   - HTTP Basic Auth `streams:diradio` (gates the auth surface against
//     scraping; per the DannyBen/audio_addict Ruby gem this header carries
//     into every subsequent request via HTTParty's class-level setting,
//     not just `Authenticate`). Missing it on vote calls produced a
//     long-debugged 403 "Invalid Session".
//   - `User-Agent: addiplay/<version>` — the Go default
//     `Go-http-client/2.0` was a candidate cause for the same 403; many
//     APIs reject blank/automated User-Agents.
//   - `Accept: application/json, */*;q=0.5` — explicit content negotiation
//     in case the server returns HTML errors only to non-JSON-accepting
//     callers (the 403 body came back as text/html).
//
// Tests that need to inspect the outgoing headers can wrap a custom
// next-RoundTripper or override `Client.HTTPClient` entirely.
type defaultHeadersTransport struct {
	basicUser, basicPass string
	userAgent            string
	next                 http.RoundTripper
}

func (t defaultHeadersTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone so we don't mutate the caller's request (HTTP RoundTripper
	// contract — request must be left untouched on retries/redirects).
	r2 := req.Clone(req.Context())
	r2.SetBasicAuth(t.basicUser, t.basicPass)
	if r2.Header.Get("User-Agent") == "" {
		r2.Header.Set("User-Agent", t.userAgent)
	}
	if r2.Header.Get("Accept") == "" {
		r2.Header.Set("Accept", "application/json, */*;q=0.5")
	}
	return t.next.RoundTrip(r2)
}

// UserAgent is the User-Agent string sent by the default transport. Tests
// override this for assertions; cmd/tui.go can update it at startup so
// release builds carry the actual binary version.
var UserAgent = "addiplay/dev (+https://github.com/dimmkirr/addiplay)"

// Client talks to AudioAddict over HTTP. The zero value is unusable; call
// NewClient. Auth state (session_key + listen_key) is owned internally —
// methods like LikeTrack read it from the stored Session instead of
// receiving it as a string parameter, which was the source of every
// "I forgot to pass the key" bug class.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	// Debug, when non-nil, receives diagnostic lines about the live API
	// shape — auth-response keys, vote request/response status,
	// Set-Cookie names. cmd/tui.go wires this to the --debug log;
	// default nil = no logging. Never log secrets verbatim
	// (listen_key, session_key, password) — only presence + lengths.
	Debug io.Writer

	// storage is where Authenticate persists newly-issued Sessions and
	// Logout clears them. nil is allowed — persistence is skipped.
	storage Storage

	// session is the in-memory auth state. Goroutines reading it during
	// vote dispatch race with the login goroutine writing it on success,
	// so it's RWMutex-guarded. SetCreds / Creds / Logout are the public
	// accessors; internal callers read via c.currentSession().
	sessionMu sync.RWMutex
	session   creds.Session
}

// NewClient returns a client with sensible defaults pointing at the real
// AudioAddict API. The Storage is used to persist successful logins and
// clear them on Logout. Pass `creds.DefaultStorage` for production;
// tests inject a memory-backed Storage so they never touch the user's
// keyring.
func NewClient(storage Storage) *Client {
	return &Client{
		BaseURL: "https://api.audioaddict.com/v1",
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: defaultHeadersTransport{
				basicUser: "streams",
				basicPass: "diradio",
				userAgent: UserAgent,
				next:      http.DefaultTransport,
			},
		},
		storage: storage,
	}
}

// SetCreds replaces the in-memory Session. UI calls this on startup
// after `creds.Load()` so the Client already has the listen_key ready
// for stream URLs and the session_key ready for votes — no caller threads
// strings into method signatures.
func (c *Client) SetCreds(s creds.Session) {
	c.sessionMu.Lock()
	c.session = s
	c.sessionMu.Unlock()
}

// Creds returns a snapshot of the in-memory Session. UI uses this for the
// header label ("👤 dmitry@..."), startup checks, and to know whether the
// login overlay should auto-pop.
func (c *Client) Creds() creds.Session {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.session
}

// Logout wipes both the in-memory Session and the persisted one. Mirrors
// the `addiplay --logout` flag (cmd/auth.go) and the in-TUI logout key.
func (c *Client) Logout() error {
	c.SetCreds(creds.Session{})
	if c.storage == nil {
		return nil
	}
	return c.storage.Clear()
}

// currentSession is the internal read accessor. Cheap mutex; called
// before every vote / stream-resolve to pick up the latest values.
func (c *Client) currentSession() creds.Session {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.session
}

// -----------------------------------------------------------------------------
// Authenticate
// -----------------------------------------------------------------------------

// Member is the subset of the authenticate response we care about. It's
// an alias for the persistent type (`creds.Session`) — Authenticate
// parses the wire format and returns a Member, the Client stores it
// in-memory, and `creds.Storage.Save` persists the same struct to disk.
// One type means we can't drift the field set between layers.
type Member = creds.Session

// authPayload mirrors the live `/v1/<network>/member_sessions` response.
// Top-level `key` is the session token used as `X-Session-Key` on
// vote/favorite write calls.
//
// History (the long path to a working vote):
//
//  1. DIMM-381 first pass — hit the legacy `/members/authenticate`
//     endpoint which surfaced listen_key as a flat field but had no
//     session key. Member.SessionKey was always "".
//  2. Second pass — switched to `/v1/<net>/member_sessions` and read
//     top-level `key`. Auth shape correct, but vote calls still got
//     403 "Invalid Session" against the live API.
//  3. Third pass (briefly) — tried `member.api_key`. Same 403.
//  4. This pass — the missing piece was Basic Auth on the vote
//     request. The DannyBen/audio_addict Ruby gem calls
//     `HTTParty.basic_auth 'streams', 'diradio'` ONCE during login,
//     which HTTParty stores on the class so every subsequent request
//     (including votes) implicitly sends `Authorization: Basic`.
//     AudioAddict's vote endpoint requires BOTH the basic creds AND
//     `X-Session-Key: <top-level key>`. With only X-Session-Key the
//     server reports the session as invalid.
type authPayload struct {
	Key    string `json:"key"` // → Member.SessionKey
	Member struct {
		ID            int64  `json:"id"`
		Email         string `json:"email"`
		ListenKey     string `json:"listen_key"`
		UserType      string `json:"user_type"`
		Subscriptions []struct {
			Status string `json:"status"`
		} `json:"subscriptions"`
	} `json:"member"`
}

// Authenticate posts the supplied creds to `/v1/<network>/member_sessions`.
// Network defaults to "di" when empty — the only network historically
// accepted by the session-creation endpoint per the DannyBen/audio_addict
// Ruby gem. Callers can pass the playing network (e.g. "rockradio") if
// future API changes require per-network session keys; the "Invalid
// Session" 403 we hit may be one such symptom.
//
// On success, the parsed Member is BOTH written to in-memory state via
// SetCreds and persisted to the configured Storage. Storage save failures
// are logged but don't fail the call — the user is logged in for this
// session even if persistence falls over.
//
// Per the Ruby gem the live API requires:
//
//   - HTTP Basic auth `streams:diradio` (sent by defaultHeadersTransport)
//   - Form body with Rails-bracket params:
//     `member_session[username]=…&member_session[password]=…`
//
// The response carries:
//
//   - top-level `key` → Member.SessionKey (X-Session-Key for vote/like
//     endpoints)
//   - nested `member.{listen_key, email, user_type, ...}` → the stream
//     key + identity
func (c *Client) Authenticate(ctx context.Context, email, password, network string) (Member, error) {
	if network == "" {
		network = "di"
	}
	form := url.Values{}
	form.Set("member_session[username]", email)
	form.Set("member_session[password]", password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/"+network+"/member_sessions",
		strings.NewReader(form.Encode()))
	if err != nil {
		return Member{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Basic auth is set by defaultHeadersTransport; no per-call SetBasicAuth.

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Member{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	c.debugAuthResponse(resp, body)

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var p authPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return Member{}, fmt.Errorf("decode authenticate: %w", err)
		}
		m := Member{
			ID:         p.Member.ID,
			Email:      p.Member.Email,
			ListenKey:  p.Member.ListenKey,
			SessionKey: p.Key,
			Premium:    p.Member.UserType == "premium",
		}
		if !m.Premium {
			for _, s := range p.Member.Subscriptions {
				if s.Status == "active" {
					m.Premium = true
					break
				}
			}
		}
		c.debugMember(m)
		if m.ListenKey == "" {
			return m, fmt.Errorf("authenticate: empty listen_key in response")
		}
		// Side effects: update in-memory state immediately so the next
		// vote/stream call sees the fresh creds, AND persist to disk so
		// the next launch doesn't need to re-login. Persistence failure
		// is logged but doesn't fail the call — the user IS logged in
		// for this session.
		c.SetCreds(m)
		if c.storage != nil {
			if err := c.storage.Save(m); err != nil {
				dlogf(c.Debug, "Authenticate: storage.Save failed (login still succeeds): %v", err)
			}
		}
		return m, nil

	case http.StatusUnauthorized, http.StatusForbidden:
		if isOAuthOnlyResponse(body) {
			return Member{}, ErrOAuthOnly
		}
		return Member{}, ErrAuth

	default:
		return Member{}, fmt.Errorf("authenticate: unexpected status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
}

// debugAuthResponse logs the live `/member_sessions` response shape to
// c.Debug — status, every top-level JSON key + value type, and Set-Cookie
// names. Originally added to diagnose the SessionKey-not-populated bug
// in DIMM-381's first pass (we were hitting a legacy `/members/
// authenticate` endpoint that returned listen_key but no session key).
// Kept around as a permanent diagnostic — auth-shape regressions are
// silent (`l` just stops working) so first-line debugging benefits from
// always having this trace under `--debug`.
//
// Secret-safe: logs presence + LENGTHS only, never the values themselves.
func (c *Client) debugAuthResponse(resp *http.Response, body []byte) {
	if c == nil || c.Debug == nil {
		return
	}
	w := c.Debug
	dlogf(w, "authenticate status=%d body_bytes=%d url=%s", resp.StatusCode, len(body), resp.Request.URL.String())
	for _, name := range []string{"Set-Cookie", "Authorization", "X-Session-Key", "X-Auth-Token", "Content-Type"} {
		for _, v := range resp.Header.Values(name) {
			cookieName := v
			if name == "Set-Cookie" {
				if eq := strings.IndexByte(v, '='); eq > 0 {
					cookieName = v[:eq]
				}
				dlogf(w, "  header %s = (name=%q, full_len=%d)", name, cookieName, len(v))
				continue
			}
			dlogf(w, "  header %s = %q", name, v)
		}
	}
	var top map[string]any
	if err := json.Unmarshal(body, &top); err == nil {
		for k, v := range top {
			length := 0
			if s, ok := v.(string); ok {
				length = len(s)
			}
			dlogf(w, "  top-level key %q type=%T len_if_string=%d", k, v, length)
		}
		// Nested member object — the most likely place a renamed
		// session_key could be hiding. Walk one level and dump field
		// names + types so we can confirm/refute the hypothesis.
		if mem, ok := top["member"].(map[string]any); ok {
			for k, v := range mem {
				length := 0
				if s, ok := v.(string); ok {
					length = len(s)
				}
				dlogf(w, "  member.%s type=%T len_if_string=%d", k, v, length)
			}
		}
		// Full masked dump — every string value under a sensitive
		// field name is replaced with <redacted:len=N>, everything
		// else is preserved verbatim. This is the line that tells us
		// the actual response shape on disagreement with our struct.
		if pretty, err := json.MarshalIndent(maskJSONSecrets(top), "", "  "); err == nil {
			dlogf(w, "  body (masked):\n%s", string(pretty))
		}
	} else {
		// Body isn't valid JSON (HTML error page? empty?). Dump the
		// raw first 512 bytes so the failure mode is obvious.
		preview := string(body)
		if len(preview) > 512 {
			preview = preview[:512] + "…"
		}
		dlogf(w, "  body is not JSON (err=%v); raw preview: %q", err, preview)
	}
}

// debugMember logs which secret fields the parsed Member ended up with.
// Only logs presence + lengths — never the values.
func (c *Client) debugMember(m Member) {
	if c == nil || c.Debug == nil {
		return
	}
	dlogf(c.Debug,
		"parsed Member: id=%d email_set=%t listen_key_len=%d session_key_len=%d premium=%t",
		m.ID, m.Email != "", len(m.ListenKey), len(m.SessionKey), m.Premium)
}

// isOAuthOnlyResponse inspects the auth-failure body for hints that the
// account has no password. AudioAddict's exact signal isn't documented; we
// look for common substrings the reference clients have seen.
func isOAuthOnlyResponse(body []byte) bool {
	s := strings.ToLower(string(body))
	for _, needle := range []string{"oauth", "social", "facebook", "google", "no password"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Channels
// -----------------------------------------------------------------------------

// Channel is one streamable channel on a network.
//
// Stays in sync with `spec.Channel` (internal/audioaddict/spec/) for the
// documented fields and extends with the undocumented fields the live API
// actually returns (asset_url, banner_url, channel_director, network_id,
// public, premium_id, channel_filter_ids). See DIMM-366 for the
// docs-vs-reality gap.
type Channel struct {
	ID    int64  `json:"id"`
	Key   string `json:"key"` // URL slug, e.g. "classicrock"
	Name  string `json:"name"`
	Image Image  `json:"images,omitempty"`

	// Descriptions — three lengths returned by the API.
	Description      string `json:"description,omitempty"`       // medium-length
	DescriptionShort string `json:"description_short,omitempty"` // 1-line tagline
	DescriptionLong  string `json:"description_long,omitempty"`  // full paragraph

	// Legacy image fields, still returned by the live API. Prefer the
	// `Image.*` template URLs (size-aware) for new code.
	AssetURL  string `json:"asset_url,omitempty"`
	BannerURL string `json:"banner_url,omitempty"`

	// Curation metadata (undocumented in upstream spec).
	ChannelDirector  string  `json:"channel_director,omitempty"`
	NetworkID        int64   `json:"network_id,omitempty"`
	Public           bool    `json:"public,omitempty"`
	PremiumID        int64   `json:"premium_id,omitempty"`
	ChannelFilterIDs []int64 `json:"channel_filter_ids,omitempty"`
}

// Image groups all artwork variants the API returns for a channel. Each
// value is an RFC-6570 URL template (`//.../X.png{?size,height,width,quality,pad}`);
// substitute params with ResolveImageURL before fetching.
//
// Upstream spec only documents Default + HorizontalBanner; the rest (Vertical,
// Square, Compact, TallBanner) are observed in live responses and verified
// against multiple channels. Track issues if a new variant appears.
type Image struct {
	Default          string `json:"default,omitempty"`
	HorizontalBanner string `json:"horizontal_banner,omitempty"`
	TallBanner       string `json:"tall_banner,omitempty"`
	Vertical         string `json:"vertical,omitempty"`
	Square           string `json:"square,omitempty"`
	Compact          string `json:"compact,omitempty"`
}

// PreferredFanartURL picks the best artwork URL for a SQUARE art frame,
// falling back through variants. Returns "" if no image is available.
// The returned URL is still a TEMPLATE — pass through ResolveImageURL to
// materialize a fetchable URL with concrete size + quality params.
//
// Square preferred because (a) album covers are 1:1, (b) our UI frame is
// 1:1 (see internal/ui/app.go), (c) downstream image-resize crops if a
// portrait or banner variant is forced into a square frame.
func (i Image) PreferredFanartURL() string {
	switch {
	case i.Square != "":
		return i.Square
	case i.Default != "":
		return i.Default
	case i.Vertical != "":
		return i.Vertical
	case i.HorizontalBanner != "":
		return i.HorizontalBanner
	}
	return ""
}

// ResolveImageURL materializes an RFC-6570 image URL template into a
// fetchable URL. AudioAddict's CDN returns templates like
//
//	//cdn-images.audioaddict.com/.../X.png{?size,height,width,quality,pad}
//
// Pass desired pixel dimensions; quality is 0–100 (75 is a sensible default).
// If the input doesn't contain "{?", it's returned unchanged (legacy URL).
// Leading "//" is upgraded to "https://" so the result is always fetchable.
func ResolveImageURL(template string, widthPx, heightPx, quality int) string {
	if template == "" {
		return ""
	}
	if strings.HasPrefix(template, "//") {
		template = "https:" + template
	}
	// Strip the {?...} template suffix if present.
	if idx := strings.Index(template, "{?"); idx >= 0 {
		template = template[:idx]
	}
	// Append our concrete params. Sort keys so the result is deterministic
	// (matters for cache keys and golden snapshots).
	parts := make([]string, 0, 3)
	if widthPx > 0 && heightPx > 0 {
		parts = append(parts, fmt.Sprintf("size=%dx%d", widthPx, heightPx))
	}
	if quality > 0 && quality <= 100 {
		parts = append(parts, fmt.Sprintf("quality=%d", quality))
	}
	if len(parts) == 0 {
		return template
	}
	return template + "?" + strings.Join(parts, "&")
}

// Channels lists every channel for the given network.
func (c *Client) Channels(ctx context.Context, network string) ([]Channel, error) {
	var out []Channel
	if err := c.getJSON(ctx, fmt.Sprintf("/%s/channels", network), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Currently playing
// -----------------------------------------------------------------------------

// Track is the currently-playing track on a channel.
type Track struct {
	ID       int64
	Artist   string // from display_artist
	Title    string // from display_title
	Track    string // raw "Artist - Title" string from the API
	Duration float64
	// ArtURL is the album-cover URL for this track when AudioAddict has
	// one (protocol-relative; pass through ResolveImageURL before fetching).
	// Empty for DJ mixes, ads, and many older tracks — caller should fall
	// back to channel.Image.PreferredFanartURL() in that case.
	ArtURL string
}

// trackHistoryItem mirrors the /v1/<net>/track_history/channel/<id> shape.
// Picked over /currently_playing because it includes art_url + the raw
// "Artist - Title" string, plus richer metadata for future features.
type trackHistoryItem struct {
	ChannelID     int64   `json:"channel_id"`
	TrackID       int64   `json:"track_id"`
	Type          string  `json:"type"` // "track" or "show" (DJ-mix programs)
	Artist        string  `json:"artist"`
	DisplayArtist string  `json:"display_artist"`
	Title         string  `json:"title"`
	DisplayTitle  string  `json:"display_title"`
	Track         string  `json:"track"` // raw "Artist - Title"
	Length        int     `json:"length"`
	Duration      float64 `json:"duration"`
	ArtURL        string  `json:"art_url"`
}

// CurrentlyPlaying returns the now-playing track for the given channel.
// Uses the /track_history/channel/<id> endpoint (most-recent track first)
// because it returns art_url; the cheaper /currently_playing endpoint
// omits artwork.
func (c *Client) CurrentlyPlaying(ctx context.Context, network string, channelID int64) (Track, error) {
	var items []trackHistoryItem
	path := fmt.Sprintf("/%s/track_history/channel/%d", network, channelID)
	if err := c.getJSON(ctx, path, &items); err != nil {
		return Track{}, err
	}
	if len(items) == 0 {
		return Track{}, nil // ad break / nothing playing
	}
	it := items[0] // most recent
	t := Track{
		ID:       it.TrackID,
		Artist:   firstNonEmpty(it.DisplayArtist, it.Artist),
		Title:    firstNonEmpty(it.DisplayTitle, it.Title),
		Track:    it.Track,
		Duration: durationOr(it.Duration, float64(it.Length)),
		ArtURL:   it.ArtURL,
	}
	if t.Track == "" {
		// Some shows have an empty `track` field; synthesize from display fields.
		switch {
		case t.Artist != "" && t.Title != "":
			t.Track = t.Artist + " - " + t.Title
		case t.Title != "":
			t.Track = t.Title
		case t.Artist != "":
			t.Track = t.Artist
		}
	}
	return t, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func durationOr(a, b float64) float64 {
	if a > 0 {
		return a
	}
	return b
}

// -----------------------------------------------------------------------------
// Stream URL
// -----------------------------------------------------------------------------

// StreamURL resolves a playable stream URL for the given (network,
// channel) using the stored listen_key. The result is the actual
// prem*.<network>:80 stream that mpv (or any HTTP audio client) can play
// directly.
//
// AudioAddict's protocol (per the tune/audio_addict reference clients) is
// a JSON resolver, NOT a static URL pattern:
//
//  1. GET http://listen.<domain>/<quality>/<channel>?<listen_key>
//  2. Server replies with a JSON array of mirror URLs:
//     ["http://prem1.di.fm:80/<channel>_hi?<key>",
//     "http://prem4.di.fm:80/<channel>_hi?<key>"]
//  3. Pick one (we use the first; mpv can fall over if it 503s).
//
// Note the `_hi` etc. suffix on the channel name in the resolved URLs —
// that's the server-side mapping from logical channel to bitrate-tagged
// stream. We don't construct it ourselves.
//
// Returns ErrListenKeyRejected on a 401/403 from the resolver — distinct
// from ErrSessionInvalid so the UI can tell that the listen_key (not the
// session_key) is the broken credential and trigger a full re-auth.
func (c *Client) StreamURL(ctx context.Context, network, channelKey string, q Quality) (string, error) {
	listenKey := c.currentSession().ListenKey
	// Confirm the channel exists; surfaces ErrUnauthorized/ErrNotFound now
	// rather than after mpv starts and silently fails.
	channels, err := c.Channels(ctx, network)
	if err != nil {
		return "", err
	}
	var found bool
	for _, ch := range channels {
		if ch.Key == channelKey {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("%w: channel %q on %q", ErrNotFound, channelKey, network)
	}

	resolverURL := fmt.Sprintf("http://listen.%s/%s/%s?%s",
		networkDomain(network), q, channelKey, url.QueryEscape(listenKey))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolverURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve stream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var mirrors []string
		if err := json.Unmarshal(body, &mirrors); err != nil {
			return "", fmt.Errorf("decode stream resolver %s: %w (body=%s)", resolverURL, err, truncate(string(body), 200))
		}
		if len(mirrors) == 0 {
			return "", fmt.Errorf("stream resolver returned empty mirror list for %s/%s", network, channelKey)
		}
		return mirrors[0], nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", ErrListenKeyRejected
	case http.StatusNotFound:
		return "", fmt.Errorf("%w: stream %s/%s/%s", ErrNotFound, network, q, channelKey)
	default:
		return "", fmt.Errorf("resolve stream %s: unexpected status %d: %s", resolverURL, resp.StatusCode, truncate(string(body), 200))
	}
}

// networkDomain returns the canonical homepage domain for a network slug —
// used in the `listen.<domain>` resolver host. Falls back to "<slug>.com"
// for unknown networks (sane default; all AudioAddict networks follow it).
func networkDomain(network string) string {
	domain := map[string]string{
		"di":             "di.fm",
		"radiotunes":     "radiotunes.com",
		"rockradio":      "rockradio.com",
		"jazzradio":      "jazzradio.com",
		"classicalradio": "classicalradio.com",
		"zenradio":       "zenradio.com",
		"frescatune":     "frescatune.com",
	}[network]
	if domain == "" {
		return network + ".com"
	}
	return domain
}

// -----------------------------------------------------------------------------
// Track votes (like / unlike)
// -----------------------------------------------------------------------------

// LikeTrack tells AudioAddict the user upvoted trackID on channelID. Auth
// is the stored Session's SessionKey (X-Session-Key header) — set via
// `SetCreds` at startup / after Authenticate. No string parameter, so
// no call site can forget it. AudioAddict treats the up/down vote as
// mutually exclusive per (track, user): posting /up overwrites a /down,
// and vice versa.
//
//	POST /v1/<network>/tracks/<track_id>/vote/<channel_id>/up
func (c *Client) LikeTrack(ctx context.Context, network string, trackID, channelID int64) error {
	path := fmt.Sprintf("/%s/tracks/%d/vote/%d/up", network, trackID, channelID)
	return c.voteRequest(ctx, http.MethodPost, path)
}

// DislikeTrack records a downvote on trackID for channelID. Symmetric to
// LikeTrack — the only difference is the `down` suffix on the URL.
//
//	POST /v1/<network>/tracks/<track_id>/vote/<channel_id>/down
func (c *Client) DislikeTrack(ctx context.Context, network string, trackID, channelID int64) error {
	path := fmt.Sprintf("/%s/tracks/%d/vote/%d/down", network, trackID, channelID)
	return c.voteRequest(ctx, http.MethodPost, path)
}

// UnlikeTrack removes a previous like OR dislike. AudioAddict's DELETE has
// no /up or /down suffix on the path — the request itself is the "clear
// vote" signal, regardless of which direction was previously set.
//
//	DELETE /v1/<network>/tracks/<track_id>/vote/<channel_id>
func (c *Client) UnlikeTrack(ctx context.Context, network string, trackID, channelID int64) error {
	path := fmt.Sprintf("/%s/tracks/%d/vote/%d", network, trackID, channelID)
	return c.voteRequest(ctx, http.MethodDelete, path)
}

// voteRequest issues a vote-API request (POST or DELETE) with the stored
// `X-Session-Key`. Returns ErrSessionInvalid on 401/403 — the UI uses
// this to distinguish vote-side auth failures (pop login + replay) from
// stream-side ones (full re-auth required).
//
// Pre-flight: if the stored SessionKey is empty, returns ErrSessionInvalid
// WITHOUT making the HTTP call. Saves a round trip and gives the UI a
// uniform error path whether the credential is empty or rejected.
func (c *Client) voteRequest(ctx context.Context, method, path string) error {
	sess := c.currentSession()
	if sess.SessionKey == "" {
		dlogf(c.Debug, "voteRequest pre-flight: SessionKey empty — skipping HTTP, returning ErrSessionInvalid")
		return ErrSessionInvalid
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Session-Key", sess.SessionKey)
	dlogf(c.Debug, "voteRequest -> %s %s session_key_len=%d", method, c.BaseURL+path, len(sess.SessionKey))
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		dlogf(c.Debug, "voteRequest transport error: %v", err)
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	dlogf(c.Debug, "voteRequest <- status=%d body_bytes=%d content_type=%q body_preview=%q",
		resp.StatusCode, len(body), resp.Header.Get("Content-Type"), truncate(string(body), 400))
	for _, name := range []string{"Set-Cookie", "WWW-Authenticate", "X-Request-Id"} {
		for _, v := range resp.Header.Values(name) {
			dlogf(c.Debug, "  voteRequest header %s = %q", name, v)
		}
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusCreated:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrSessionInvalid
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrRateLimit
	default:
		return fmt.Errorf("%s %s: unexpected status %d: %s", method, path, resp.StatusCode, truncate(string(body), 200))
	}
}

// -----------------------------------------------------------------------------
// Track info — for restoring vote state across sessions
// -----------------------------------------------------------------------------

// TrackInfo is the subset of `GET /v1/<network>/tracks/<id>` we care
// about. The full response carries display metadata, waveform URL,
// release info, and the votes block we use to decide whether to render
// a filled / outline / broken heart on a track the user voted on
// previously (in a past addiplay session, or via di.fm's web player).
type TrackInfo struct {
	ID            int64    `json:"id"`
	Length        int      `json:"length"`
	DisplayTitle  string   `json:"display_title"`
	DisplayArtist string   `json:"display_artist"`
	Track         string   `json:"track"`
	Votes         VoteInfo `json:"votes"`
}

// VoteInfo carries the up/down totals plus two bloom filters: one per
// direction. To check whether a specific member voted, hash their
// member_id against the filter and verify all bits are set. AudioAddict's
// web app and mobile clients use this to render the heart state — there
// is no `voted_by_me` field returned by the API.
type VoteInfo struct {
	Up           int          `json:"up"`
	Down         int          `json:"down"`
	WhoUpvoted   *BloomFilter `json:"who_upvoted,omitempty"`
	WhoDownvoted *BloomFilter `json:"who_downvoted,omitempty"`
}

// BloomFilter is the wire representation of the vote-membership filter:
//
//	{"size": 7117, "hashes": 30, "seed": 1782756909, "bits": [1234, 5678, ...]}
//
//   - size — the bit count (filter modulus). 7117 is typical for a track
//     with a few thousand voters.
//   - hashes — number of hash functions to run per query. 30 is unusually
//     high for a bloom filter; it gives a very low false-positive rate
//     when the populated count is small.
//   - seed — a uint32 (changes per response — likely a unix timestamp).
//   - bits — the bit array packed into uint32 words.
type BloomFilter struct {
	Size   uint32   `json:"size"`
	Hashes int      `json:"hashes"`
	Seed   uint32   `json:"seed"`
	Bits   []uint32 `json:"bits"`
}

// Contains reports whether memberID is encoded in the bloom filter.
// The algorithm is verbatim from di.fm's web player JavaScript
// (`di.math.BloomFilter` in their `application-*.js` bundle, located
// 2026-06-29 via DevTools — search "who_upvoted"):
//
//	indexesFor(member) {
//	    positions = []
//	    for i in 0..hashes-1:
//	        positions.push(Zlib.crc32(member + ":" + (i + seed)) % size)
//	    return positions
//	}
//	test(member) { return all positions are set in bits }
//
// Key facts:
//
//   - Hash: standard IEEE CRC32 (the JS bundle ships an inline table
//     whose first non-zero entry is 1996959894 = IEEE polynomial
//     0xEDB88320 reversed).
//   - Key format: ASCII string `"<memberID>:<i+seed>"`, NOT little-endian
//     binary of the ID, NOT just the ID alone. The colon separator and
//     the per-iteration `i+seed` suffix are critical.
//   - Bit access: word `p/32`, mask `1 << (p%32)` — little-endian within
//     each uint32 word.
//
// Returns false on a nil receiver or a filter with no bits / no hashes
// (the API returns null `who_downvoted` for tracks with zero dislikes).
func (b *BloomFilter) Contains(memberID int64) bool {
	if b == nil || b.Size == 0 || b.Hashes <= 0 || len(b.Bits) == 0 {
		return false
	}
	idStr := strconv.FormatInt(memberID, 10)
	for i := 0; i < b.Hashes; i++ {
		// Key = "<memberID>:<i+seed>" — exact JS reproduction.
		key := idStr + ":" + strconv.FormatUint(uint64(b.Seed)+uint64(i), 10)
		sum := crc32.ChecksumIEEE([]byte(key))
		pos := sum % b.Size
		word := pos / 32
		bit := uint32(1) << (pos % 32)
		if word >= uint32(len(b.Bits)) {
			return false
		}
		if b.Bits[word]&bit == 0 {
			return false
		}
	}
	return true
}

// FetchTrack does `GET /v1/<network>/tracks/<trackID>`. Public read —
// no session_key required; basic auth is applied by the transport.
func (c *Client) FetchTrack(ctx context.Context, network string, trackID int64) (*TrackInfo, error) {
	var out TrackInfo
	path := fmt.Sprintf("/%s/tracks/%d", network, trackID)
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	dlogf(c.Debug, "FetchTrack OK network=%s trackID=%d title=%q artist=%q up=%d down=%d who_up_size=%d who_up_seed=%d who_up_bits=%d",
		network, trackID, out.DisplayTitle, out.DisplayArtist,
		out.Votes.Up, out.Votes.Down,
		bloomSize(out.Votes.WhoUpvoted), bloomSeed(out.Votes.WhoUpvoted), bloomBitsLen(out.Votes.WhoUpvoted))
	return &out, nil
}

func bloomSize(b *BloomFilter) uint32 {
	if b == nil {
		return 0
	}
	return b.Size
}
func bloomSeed(b *BloomFilter) uint32 {
	if b == nil {
		return 0
	}
	return b.Seed
}
func bloomBitsLen(b *BloomFilter) int {
	if b == nil {
		return 0
	}
	return len(b.Bits)
}

// -----------------------------------------------------------------------------
// Common GET helper
// -----------------------------------------------------------------------------

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrRateLimit
	default:
		return fmt.Errorf("%s: unexpected status %d: %s", path, resp.StatusCode, truncate(string(body), 200))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
