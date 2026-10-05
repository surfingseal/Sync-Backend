package client

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/model"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type OfflinePool struct {
	Name     string               `json:"name"`
	Keywords []string             `json:"keywords"`
	Videos   []model.YouTubeVideo `json:"videos"`
}
type ReplayDocument struct {
	RecordedAt time.Time     `json:"recorded_at"`
	Pools      []OfflinePool `json:"pools"`
}
type OfflineMusicClient struct {
	RecordedAt time.Time
	Mode       string
	Pools      []OfflinePool
}

func (c *OfflineMusicClient) DataMode() string { return c.Mode }
func NewReplayMusicClient(path string) (*OfflineMusicClient, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("replay file unavailable")
	}
	defer f.Close()
	var doc ReplayDocument
	if json.NewDecoder(f).Decode(&doc) != nil || len(doc.Pools) == 0 || doc.RecordedAt.IsZero() || time.Since(doc.RecordedAt) > 30*24*time.Hour || doc.RecordedAt.After(time.Now().Add(time.Minute)) {
		return nil, fmt.Errorf("invalid or expired replay data")
	}
	return &OfflineMusicClient{Mode: "replay", RecordedAt: doc.RecordedAt, Pools: doc.Pools}, nil
}
func NewFixtureMusicClient() *OfflineMusicClient {
	c := &OfflineMusicClient{Mode: "fixture"}
	for _, name := range []string{"sunset", "cafe", "city-night", "ocean"} {
		pool := OfflinePool{Name: name, Keywords: []string{name}}
		for i := 0; i < 20; i++ {
			kind := "official lyrics"
			if i%4 == 0 {
				kind = "instrumental"
			}
			likes, comments := uint64(12000), uint64(400)
			pool.Videos = append(pool.Videos, model.YouTubeVideo{VideoID: fmt.Sprintf("fixture_%s_%02d", name, i), Title: fmt.Sprintf("SYNTHETIC %s calm warm indie pop %s %d", name, kind, i), ChannelTitle: fmt.Sprintf("Fixture Channel %d - Topic", i), ChannelID: fmt.Sprint(i), CategoryID: "10", DurationSeconds: 210, Public: true, Embeddable: true, ViewCount: 2000000, LikeCount: &likes, CommentCount: &comments, LicensedContent: true, DefaultAudioLanguage: "en"})
		}
		c.Pools = append(c.Pools, pool)
	}
	return c
}
func (c *OfflineMusicClient) SearchMusic(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	if c.Mode == "replay" && (c.RecordedAt.IsZero() || time.Since(c.RecordedAt) > 30*24*time.Hour) {
		return nil, ErrMusicSearch
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.MaxResults < 1 || q.MaxResults > 50 {
		return nil, ErrMusicSearch
	}
	if m := retrievalMetrics(ctx); m != nil {
		if c.Mode == "fixture" {
			m.FixtureRequests++
		} else {
			m.ReplayRequests++
		}
	}
	// Local token matching is a replay simulation, not a counterfactual YouTube result.
	type scored struct {
		v     model.YouTubeVideo
		score int
	}
	rows := []scored{}
	seen := map[string]bool{}
	for _, p := range c.Pools {
		for _, v := range p.Videos {
			if seen[v.VideoID] {
				continue
			}
			seen[v.VideoID] = true
			score := 0
			text := strings.ToLower(v.Title + " " + v.Description + " " + strings.Join(p.Keywords, " "))
			for _, word := range strings.Fields(strings.ToLower(q.Text)) {
				if strings.Contains(text, word) {
					score++
				}
			}
			rows = append(rows, scored{v, score})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].score > rows[j].score })
	result := []model.YouTubeSearchResult{}
	for _, row := range rows[:min(len(rows), int(q.MaxResults))] {
		result = append(result, model.YouTubeSearchResult{VideoID: row.v.VideoID})
	}
	return result, nil
}
func (c *OfflineMusicClient) GetVideos(ctx context.Context, ids []string) ([]model.YouTubeVideo, error) {
	if c.Mode == "replay" && (c.RecordedAt.IsZero() || time.Since(c.RecordedAt) > 30*24*time.Hour) {
		return nil, ErrMusicSearch
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := []model.YouTubeVideo{}
	for _, pool := range c.Pools {
		for _, v := range pool.Videos {
			if want[v.VideoID] {
				out = append(out, v)
				delete(want, v.VideoID)
			}
		}
	}
	return out, nil
}
