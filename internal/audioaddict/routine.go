package audioaddict

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"sync"
)

// RoutineTrack is a single on-demand track from the channel routine API.
// The routine endpoint returns artist/title/track as objects (not strings),
// unlike the track_history endpoint. We use json.RawMessage and extract
// display-ready values from display_artist / display_title instead.
type RoutineTrack struct {
	ID      int64 `json:"id"`
	TrackID int64 `json:"track_id"`
	Content struct {
		Length      float64 `json:"length"`
		Interactive bool    `json:"interactive"`
		Assets      []struct {
			URL string `json:"url"`
		} `json:"assets"`
	} `json:"content"`
	Track         json.RawMessage `json:"track"`
	Artist        json.RawMessage `json:"artist"`
	DisplayArtist string          `json:"display_artist"`
	Title         json.RawMessage `json:"title"`
	DisplayTitle  string          `json:"display_title"`
	ArtURL        string          `json:"art_url"`
	Images        Image           `json:"images"`
	Length        float64         `json:"length"`
}

// AudioURL returns the CDN URL for this track's audio, unescaping HTML entities
// and normalising protocol-relative URLs to https.
func (rt RoutineTrack) AudioURL() string {
	if len(rt.Content.Assets) == 0 {
		return ""
	}
	u := html.UnescapeString(rt.Content.Assets[0].URL)
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	return u
}

// ToTrack converts a RoutineTrack to the UI's Track type.
func (rt RoutineTrack) ToTrack() Track {
	artist := firstNonEmpty(rt.DisplayArtist, rawString(rt.Artist))
	title := firstNonEmpty(rt.DisplayTitle, rawString(rt.Title))
	track := rawString(rt.Track)
	if track == "" {
		switch {
		case artist != "" && title != "":
			track = artist + " - " + title
		case title != "":
			track = title
		case artist != "":
			track = artist
		}
	}
	id := rt.TrackID
	if id == 0 {
		id = rt.ID
	}
	artURL := rt.ArtURL
	if artURL == "" {
		artURL = rt.Images.PreferredFanartURL()
	}
	return Track{
		ID:       id,
		Artist:   artist,
		Title:    title,
		Track:    track,
		Duration: durationOr(rt.Content.Length, rt.Length),
		ArtURL:   artURL,
	}
}

// rawString extracts a display string from a json.RawMessage that may be
// a JSON string, an object with a "name" field, or null/absent.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// Try plain string first.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	// Try object with "name" key.
	var obj struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Name != "" {
		return obj.Name
	}
	return ""
}

// routineResponse is the wire format of GET /v1/<network>/routines/channel/<id>.
type routineResponse struct {
	AudioToken string         `json:"audio_token"`
	ExpiresOn  string         `json:"expires_on"`
	RoutineID  json.Number    `json:"routine_id"`
	ChannelID  int64          `json:"channel_id"`
	Tracks     []RoutineTrack `json:"tracks"`
}

// FetchRoutine calls GET /v1/<network>/routines/channel/<channelID> with
// the stored audio_token. Returns the batch of on-demand tracks. tuneIn=true
// on the first call for a channel (signals a new listening session); false
// when refilling the queue.
func (c *Client) FetchRoutine(ctx context.Context, network string, channelID int64, tuneIn bool) ([]RoutineTrack, error) {
	sess := c.currentSession()
	if sess.AudioToken == "" {
		return nil, fmt.Errorf("audioaddict: audio_token not available — re-login required")
	}
	path := fmt.Sprintf("/%s/routines/channel/%d", network, channelID)
	tuneInStr := "false"
	if tuneIn {
		tuneInStr = "true"
	}
	fullPath := path + "?" + url.Values{
		"audio_token": {sess.AudioToken},
		"tune_in":     {tuneInStr},
	}.Encode()

	dlogf(c.Debug, "FetchRoutine -> GET %s%s", c.BaseURL, fullPath)
	var rawBody json.RawMessage
	if err := c.getJSON(ctx, fullPath, &rawBody); err != nil {
		dlogf(c.Debug, "FetchRoutine FAIL (raw) err=%v", err)
		return nil, err
	}
	dlogf(c.Debug, "FetchRoutine raw body (first 2000): %s", truncate(string(rawBody), 2000))
	var resp routineResponse
	if err := json.Unmarshal(rawBody, &resp); err != nil {
		dlogf(c.Debug, "FetchRoutine FAIL err=%v", err)
		return nil, err
	}
	dlogf(c.Debug, "FetchRoutine OK tracks=%d routine_id=%s expires=%s",
		len(resp.Tracks), resp.RoutineID, resp.ExpiresOn)
	return resp.Tracks, nil
}

// TrackQueue manages a FIFO of on-demand tracks for a single channel.
// The caller feeds tracks via Append and consumes via Next. Goroutine-safe.
type TrackQueue struct {
	mu     sync.Mutex
	tracks []RoutineTrack
	pos    int
}

// NewTrackQueue creates an empty queue.
func NewTrackQueue() *TrackQueue {
	return &TrackQueue{}
}

// Append adds tracks to the end of the queue.
func (q *TrackQueue) Append(tracks []RoutineTrack) {
	q.mu.Lock()
	q.tracks = append(q.tracks, tracks...)
	q.mu.Unlock()
}

// Next returns the next track and advances the position.
// Returns false if the queue is exhausted.
func (q *TrackQueue) Next() (RoutineTrack, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pos >= len(q.tracks) {
		return RoutineTrack{}, false
	}
	t := q.tracks[q.pos]
	q.pos++
	return t, true
}

// Current returns the track at the current position (last returned by Next).
func (q *TrackQueue) Current() (RoutineTrack, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pos == 0 || q.pos > len(q.tracks) {
		return RoutineTrack{}, false
	}
	return q.tracks[q.pos-1], true
}

// Remaining returns how many tracks are queued after the current position.
func (q *TrackQueue) Remaining() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	r := len(q.tracks) - q.pos
	if r < 0 {
		return 0
	}
	return r
}

// Position returns the 1-based index of the current track and total count.
func (q *TrackQueue) Position() (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pos, len(q.tracks)
}

// Reset clears the queue.
func (q *TrackQueue) Reset() {
	q.mu.Lock()
	q.tracks = nil
	q.pos = 0
	q.mu.Unlock()
}
