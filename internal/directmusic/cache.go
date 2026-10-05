// Package directmusic implements an isolated image-to-track experiment. It does
// not use Last.fm, taxonomy-based discovery, production ranking or playlist writes.
package directmusic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type CacheEntry struct {
	VideoID  string    `json:"video_id,omitempty"`
	Negative bool      `json:"negative"`
	Status   string    `json:"status,omitempty"`
	Expires  time.Time `json:"expires"`
}
type Cache struct {
	mu      sync.Mutex
	entries map[string]CacheEntry
	path    string
	now     func() time.Time
}

func NewCache(path string) (*Cache, error) {
	c := &Cache{entries: map[string]CacheEntry{}, path: path, now: time.Now}
	if path != "" {
		b, e := os.ReadFile(path)
		if e == nil {
			if json.Unmarshal(b, &c.entries) != nil || c.entries == nil {
				return nil, fmt.Errorf("invalid resolver cache")
			}
		} else if !os.IsNotExist(e) {
			return nil, fmt.Errorf("cannot read resolver cache")
		}
	}
	return c, nil
}
func (c *Cache) Get(k string) (CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[k]
	return v, ok && c.now().Before(v.Expires)
}
func (c *Cache) Put(k string, e CacheEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[k] = e
	if c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return fmt.Errorf("cannot create cache directory")
	}
	b, _ := json.MarshalIndent(c.entries, "", "  ")
	f, err := os.CreateTemp(filepath.Dir(c.path), ".resolver-*")
	if err != nil {
		return fmt.Errorf("cannot write cache")
	}
	p := f.Name()
	defer os.Remove(p)
	if _, err = f.Write(b); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err == nil {
		err = os.Rename(p, c.path)
	}
	if err != nil {
		return fmt.Errorf("cannot persist resolver cache")
	}
	return nil
}
func cacheKey(artist, title string, c Config) string {
	return policyCacheKey(artist, title, c, ResolverVersion)
}
func legacyCacheKey(artist, title string, c Config) string {
	return policyCacheKey(artist, title, c, LegacyResolverVersion)
}
func policyCacheKey(artist, title string, c Config, version string) string {
	b, _ := json.Marshal([]any{version, NormalizeKey(artist), NormalizeKey(title), c.Region, c.MinDuration, c.MaxDuration, c.SearchResults})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
