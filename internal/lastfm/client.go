// Package lastfm is an isolated public tag-audit client. It is not wired to recommendation routes.
package lastfm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrTimeout  = errors.New("Last.fm timeout")
	ErrNetwork  = errors.New("Last.fm network error")
	ErrAPI      = errors.New("Last.fm API error")
	ErrHTTP     = errors.New("Last.fm HTTP error")
	ErrResponse = errors.New("Last.fm invalid response")
)

type Error struct {
	Kind         error
	Method       string
	Status, Code int
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s method=%s status=%d code=%d", e.Kind, e.Method, e.Status, e.Code)
}
func (e *Error) Unwrap() error { return e.Kind }

// Optional numeric fields accept the documented JSON number/string variants.
type Number int64

func (n *Number) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b)
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		*n = Number(i)
	}
	return err
}

type Tag struct {
	Name  string  `json:"name"`
	Count *Number `json:"count,omitempty"`
	Reach *Number `json:"reach,omitempty"`
	URL   string  `json:"url,omitempty"`
}
type TagInfo struct {
	Name     string  `json:"name"`
	URL      string  `json:"url,omitempty"`
	Reach    *Number `json:"reach,omitempty"`
	Taggings *Number `json:"taggings,omitempty"`
	Total    *Number `json:"total,omitempty"`
	Wiki     struct {
		Summary   string `json:"summary"`
		Content   string `json:"content"`
		Published string `json:"published"`
	} `json:"wiki"`
}
type Track struct {
	Name   string `json:"name"`
	MBID   string `json:"mbid,omitempty"`
	URL    string `json:"url"`
	Artist struct {
		Name string `json:"name"`
		MBID string `json:"mbid,omitempty"`
		URL  string `json:"url,omitempty"`
	} `json:"artist"`
	Attr struct {
		Rank string `json:"rank"`
	} `json:"@attr"`
}
type Event struct {
	Method        string    `json:"method"`
	Tag           string    `json:"tag,omitempty"`
	At            time.Time `json:"timestamp"`
	MS            float64   `json:"latency_ms"`
	Cached        bool      `json:"cached"`
	Success       bool      `json:"success"`
	HTTPAttempted bool      `json:"http_attempted"`
}
type Client struct {
	Key, BaseURL, UserAgent, CacheDir string
	HTTP                              *http.Client
	Timeout, TTL                      time.Duration
	Fresh                             bool
	Events                            []Event
}

func New(key string) *Client {
	return &Client{Key: key, BaseURL: "https://ws.audioscrobbler.com/2.0/", UserAgent: "SyncTagAudit/1.0 (Last.fm vocabulary research)", HTTP: &http.Client{Timeout: 15 * time.Second}, Timeout: 15 * time.Second, TTL: 24 * time.Hour}
}
func CacheKey(base, method, tag string, limit int) string {
	v := url.Values{"base": {base}, "method": {method}, "tag": {Normalize(tag)}, "limit": {strconv.Itoa(limit)}, "format": {"json"}, "page": {"1"}}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(v.Encode())))
}

type cacheEntry struct {
	At   time.Time       `json:"fetched_at"`
	Data json.RawMessage `json:"data"`
}

func (c *Client) request(ctx context.Context, method, tag string, limit int, out any) (err error) {
	start := time.Now()
	event := Event{Method: method, Tag: tag, At: start.UTC()}
	defer func() {
		event.MS = float64(time.Since(start)) / float64(time.Millisecond)
		event.Success = err == nil
		c.Events = append(c.Events, event)
	}()
	if strings.TrimSpace(c.Key) == "" {
		return &Error{ErrAPI, method, 0, 10}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	key := CacheKey(c.BaseURL, method, tag, limit)
	path := filepath.Join(c.CacheDir, key+".json")
	if c.CacheDir != "" && !c.Fresh {
		var entry cacheEntry
		if b, e := os.ReadFile(path); e == nil && json.Unmarshal(b, &entry) == nil && time.Since(entry.At) < c.TTL {
			if e = json.Unmarshal(entry.Data, out); e == nil {
				event.Cached = true
				return nil
			}
		}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u, e := url.Parse(c.BaseURL)
	if e != nil {
		return &Error{ErrResponse, method, 0, 0}
	}
	q := u.Query()
	q.Set("method", method)
	q.Set("api_key", c.Key)
	q.Set("format", "json")
	if tag != "" {
		q.Set("tag", tag)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
		q.Set("page", "1")
	}
	u.RawQuery = q.Encode()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return &Error{ErrResponse, method, 0, 0}
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: timeout}
	}
	event.HTTPAttempted = true
	resp, e := h.Do(req)
	if e != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		var n net.Error
		if ctx.Err() != nil || errors.As(e, &n) && n.Timeout() {
			return &Error{ErrTimeout, method, 0, 0}
		}
		return &Error{ErrNetwork, method, 0, 0}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{ErrHTTP, method, resp.StatusCode, 0}
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if e != nil && ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return &Error{ErrTimeout, method, resp.StatusCode, 0}
	}
	if e != nil || len(raw) > 4*1024*1024 || strings.Contains(string(raw), c.Key) {
		return &Error{ErrResponse, method, resp.StatusCode, 0}
	}
	var api struct {
		Code Number `json:"error"`
	}
	if json.Unmarshal(raw, &api) != nil {
		return &Error{ErrResponse, method, resp.StatusCode, 0}
	}
	if api.Code != 0 {
		return &Error{ErrAPI, method, resp.StatusCode, int(api.Code)}
	}
	if json.Unmarshal(raw, out) != nil {
		return &Error{ErrResponse, method, resp.StatusCode, 0}
	}
	if c.CacheDir != "" {
		if e = os.MkdirAll(c.CacheDir, 0700); e != nil {
			return fmt.Errorf("Last.fm cache directory unavailable")
		}
		b, _ := json.Marshal(cacheEntry{time.Now().UTC(), raw})
		if e = os.WriteFile(path, b, 0600); e != nil {
			return fmt.Errorf("Last.fm cache write failed")
		}
	}
	return nil
}
func (c *Client) GetTopTags(ctx context.Context) ([]Tag, error) {
	var r struct {
		Tags *struct {
			Tag []Tag `json:"tag"`
		} `json:"toptags"`
	}
	if err := c.request(ctx, "tag.getTopTags", "", 0, &r); err != nil {
		return nil, err
	}
	if r.Tags == nil {
		return nil, &Error{ErrResponse, "tag.getTopTags", 0, 0}
	}
	return r.Tags.Tag, nil
}
func (c *Client) GetInfo(ctx context.Context, tag string) (*TagInfo, error) {
	var r struct {
		Tag *TagInfo `json:"tag"`
	}
	if err := c.request(ctx, "tag.getInfo", tag, 0, &r); err != nil {
		return nil, err
	}
	if r.Tag == nil {
		return nil, &Error{ErrResponse, "tag.getInfo", 0, 0}
	}
	return r.Tag, nil
}
func (c *Client) GetTopTracks(ctx context.Context, tag string, limit int) ([]Track, error) {
	if limit < 1 || limit > 50 {
		return nil, fmt.Errorf("Last.fm audit limit must be 1..50")
	}
	var r struct {
		Tracks *struct {
			Track []Track `json:"track"`
		} `json:"tracks"`
	}
	if err := c.request(ctx, "tag.getTopTracks", tag, limit, &r); err != nil {
		return nil, err
	}
	if r.Tracks == nil {
		return nil, &Error{ErrResponse, "tag.getTopTracks", 0, 0}
	}
	return r.Tracks.Track, nil
}
