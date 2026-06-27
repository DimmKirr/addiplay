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
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

// Public errors. Callers may errors.Is() against these.
var (
	// ErrAuth — login endpoint rejected the supplied email/password.
	ErrAuth = errors.New("audioaddict: invalid credentials")
	// ErrOAuthOnly — account has no password (Google/Facebook sign-up).
	ErrOAuthOnly = errors.New("audioaddict: account uses social login (no password)")
	// ErrUnauthorized — 401/403 from a non-auth endpoint (stale listen_key).
	ErrUnauthorized = errors.New("audioaddict: listen_key rejected; re-login required")
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

// Client talks to AudioAddict over HTTP. The zero value is unusable; call
// NewClient.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewClient returns a client with sensible defaults pointing at the real API.
// Tests inject a custom BaseURL.
func NewClient() *Client {
	return &Client{
		BaseURL:    "https://api.audioaddict.com/v1",
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// -----------------------------------------------------------------------------
// Authenticate
// -----------------------------------------------------------------------------

// Member is the subset of the authenticate response we care about.
type Member struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	ListenKey string `json:"listen_key"`
	Premium   bool   `json:"-"` // derived from the subscription block
}

// authPayload mirrors the (flat) shape of the AudioAddict authenticate
// response. The reference Ruby/Python clients show a `member_session[*]`
// /  `{"member":{...}}` Devise-style shape; the live API as of 2026-06-26
// instead accepts flat `username`/`password` params and returns member
// fields at the top level. Fields not in this struct are ignored.
type authPayload struct {
	ID            int64  `json:"id"`
	Email         string `json:"email"`
	ListenKey     string `json:"listen_key"`
	UserType      string `json:"user_type"`
	Subscriptions []struct {
		Status string `json:"status"`
	} `json:"subscriptions"`
}

// Authenticate posts the supplied creds to /v1/di/members/authenticate (the
// "di" network is conventional — the same listen_key works on every network).
// On success the returned Member.ListenKey is what every subsequent call
// uses.
func (c *Client) Authenticate(ctx context.Context, email, password string) (Member, error) {
	form := url.Values{}
	form.Set("username", email)
	form.Set("password", password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/di/members/authenticate",
		strings.NewReader(form.Encode()))
	if err != nil {
		return Member{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Member{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var p authPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return Member{}, fmt.Errorf("decode authenticate: %w", err)
		}
		m := Member{
			ID:        p.ID,
			Email:     p.Email,
			ListenKey: p.ListenKey,
			Premium:   p.UserType == "premium",
		}
		if !m.Premium {
			for _, s := range p.Subscriptions {
				if s.Status == "active" {
					m.Premium = true
					break
				}
			}
		}
		if m.ListenKey == "" {
			return m, fmt.Errorf("authenticate: empty listen_key in response")
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
// channel) using listenKey. The result is the actual prem*.<network>:80
// stream that mpv (or any HTTP audio client) can play directly.
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
func (c *Client) StreamURL(ctx context.Context, network, channelKey, listenKey string, q Quality) (string, error) {
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
		return "", ErrUnauthorized
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
