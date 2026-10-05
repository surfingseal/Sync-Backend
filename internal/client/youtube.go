package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"example.com/sync/internal/model"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

// Optional observation exposes only public search snippets, never request URLs or credentials.
type YouTubeSearchSnippet struct {
	ChannelID    string `json:"channel_id"`
	PublishedAt  string `json:"published_at"`
	VideoID      string `json:"video_id"`
	Title        string `json:"title"`
	ChannelTitle string `json:"channel_title"`
}
type YouTubeSearchObserver func(model.MusicSearchQuery, []YouTubeSearchSnippet)
type YouTubeClient struct {
	service  *youtube.Service
	observer YouTubeSearchObserver
}

var _ MusicSearchClient = (*YouTubeClient)(nil)

func NewYouTubeClient(ctx context.Context, key string, observers ...YouTubeSearchObserver) (*YouTubeClient, error) {
	if strings.TrimSpace(key) == "" {
		return &YouTubeClient{}, nil
	}
	// API-key auth is independent of Vertex ADC. No service-account options.
	sdk, err := youtube.NewService(ctx, option.WithAPIKey(key), option.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return nil, fmt.Errorf("%w: client initialization", ErrMusicSearch)
	}
	c := &YouTubeClient{service: sdk}
	if len(observers) > 0 {
		c.observer = observers[0]
	}
	return c, nil
}
func (y *YouTubeClient) SearchMusic(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	if y.service == nil {
		return nil, ErrMusicNotConfigured
	}
	if q.MaxResults < 1 || q.MaxResults > 50 || strings.TrimSpace(q.Text) == "" {
		return nil, ErrMusicSearch
	}
	order := q.Order
	if order == "" {
		order = "relevance"
	}
	if order != "relevance" && order != "viewCount" {
		return nil, ErrMusicSearch
	}
	start := time.Now()
	response, err := y.service.Search.List([]string{"snippet"}).Type("video").VideoCategoryId(model.MusicCategoryID).RegionCode(q.Region).RelevanceLanguage(q.RelevanceLanguage).VideoEmbeddable("true").VideoSyndicated("true").SafeSearch("moderate").MaxResults(q.MaxResults).Order(order).Q(q.Text).Context(ctx).Do()
	log.Printf("youtube method=search.list latency=%s success=%t", time.Since(start), err == nil)
	if err != nil {
		return nil, mapYouTubeError(ctx, err)
	}
	snippets := []YouTubeSearchSnippet{}
	result := make([]model.YouTubeSearchResult, 0, len(response.Items))
	for _, item := range response.Items {
		if item != nil && item.Id != nil && item.Id.Kind == "youtube#video" && item.Id.VideoId != "" {
			result = append(result, model.YouTubeSearchResult{VideoID: item.Id.VideoId})
			snippet := YouTubeSearchSnippet{VideoID: item.Id.VideoId}
			if item.Snippet != nil {
				snippet.ChannelID = item.Snippet.ChannelId
				snippet.PublishedAt = item.Snippet.PublishedAt
				snippet.Title = item.Snippet.Title
				snippet.ChannelTitle = item.Snippet.ChannelTitle
			}
			snippets = append(snippets, snippet)
		}
	}
	if y.observer != nil {
		y.observer(q, snippets)
	}
	return result, nil
}
func (y *YouTubeClient) GetVideos(ctx context.Context, ids []string) ([]model.YouTubeVideo, error) {
	if len(ids) == 0 {
		return []model.YouTubeVideo{}, nil
	}
	if y.service == nil {
		return nil, ErrMusicNotConfigured
	}
	if len(ids) > 50 {
		return nil, ErrMusicSearch
	}
	start := time.Now()
	response, err := y.service.Videos.List([]string{"snippet", "contentDetails", "status", "statistics"}).Id(ids...).Context(ctx).Do()
	returned := 0
	if response != nil {
		returned = len(response.Items)
	}
	log.Printf("youtube method=videos.list batch=%d details_returned=%d latency=%s success=%t", len(ids), returned, time.Since(start), err == nil)
	if err != nil {
		return nil, mapYouTubeError(ctx, err)
	}
	result := make([]model.YouTubeVideo, 0, len(response.Items))
	for _, v := range response.Items {
		if v == nil || v.Snippet == nil || v.ContentDetails == nil || v.Status == nil {
			continue
		}
		seconds, err := parseYouTubeDuration(v.ContentDetails.Duration)
		if err != nil {
			continue
		}
		item := model.YouTubeVideo{Description: v.Snippet.Description, ChannelID: v.Snippet.ChannelId, PublishedAt: v.Snippet.PublishedAt, LicensedContent: v.ContentDetails.LicensedContent, VideoID: v.Id, Title: v.Snippet.Title, ChannelTitle: v.Snippet.ChannelTitle, CategoryID: v.Snippet.CategoryId, ThumbnailURL: thumbnailURL(v.Snippet.Thumbnails), DurationSeconds: seconds, Embeddable: v.Status.Embeddable, Public: v.Status.PrivacyStatus == "public", Live: v.Snippet.LiveBroadcastContent == "live" || v.Snippet.LiveBroadcastContent == "upcoming", DefaultAudioLanguage: v.Snippet.DefaultAudioLanguage, DefaultLanguage: v.Snippet.DefaultLanguage}
		if v.Statistics != nil {
			item.ViewCount = v.Statistics.ViewCount
			if v.Statistics.CommentCount > 0 {
				count := v.Statistics.CommentCount
				item.CommentCount = &count
			}
			if v.Statistics.LikeCount > 0 {
				likes := v.Statistics.LikeCount
				item.LikeCount = &likes
			}
		}
		if rr := v.ContentDetails.RegionRestriction; rr != nil {
			item.AllowedRegions = rr.Allowed
			item.BlockedRegions = rr.Blocked
		}
		result = append(result, item)
	}
	return result, nil
}
func thumbnailURL(t *youtube.ThumbnailDetails) string {
	if t == nil {
		return ""
	}
	for _, image := range []*youtube.Thumbnail{t.High, t.Medium, t.Default} {
		if image != nil && image.Url != "" {
			return image.Url
		}
	}
	return ""
}
func mapYouTubeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return context.DeadlineExceeded
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		log.Printf("youtube failure http_status=%d", apiErr.Code)
		if apiErr.Code == http.StatusTooManyRequests {
			return ErrYouTubeQuota
		}
		for _, detail := range apiErr.Errors {
			switch detail.Reason {
			case "quotaExceeded", "dailyLimitExceeded", "rateLimitExceeded", "userRateLimitExceeded":
				return ErrYouTubeQuota
			}
		}
		// Never wrap Google raw bodies/URLs: they may include API keys.
		return fmt.Errorf("%w: YouTube HTTP %d", ErrMusicSearch, apiErr.Code)
	}
	return fmt.Errorf("%w: transport failure (cause=%T)", ErrMusicSearch, err)
}

var durationPattern = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

func parseYouTubeDuration(s string) (int64, error) {
	if len(s) > 64 {
		return 0, ErrMusicSearch
	}
	parts := durationPattern.FindStringSubmatch(s)
	if parts == nil {
		return 0, ErrMusicSearch
	}
	var total int64
	found := false
	for i, multiplier := range []int64{86400, 3600, 60, 1} {
		if parts[i+1] == "" {
			continue
		}
		found = true
		value, err := strconv.ParseInt(parts[i+1], 10, 64)
		if err != nil || value > (math.MaxInt64-total)/multiplier {
			return 0, ErrMusicSearch
		}
		total += value * multiplier
	}
	if !found {
		return 0, ErrMusicSearch
	}
	return total, nil
}
