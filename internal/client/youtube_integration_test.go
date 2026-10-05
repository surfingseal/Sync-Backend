package client_test

import (
	"context"
	"os"
	"testing"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
)

type fixedIntegrationQueries struct{}

func (fixedIntegrationQueries) GenerateMusicQueries(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error) {
	return []string{"korean acoustic calm song official audio", "english indie pop calm song official audio"}, nil
}
func TestYouTubeIntegration(t *testing.T) {
	if os.Getenv("YOUTUBE_INTEGRATION_TEST") != "1" {
		t.Skip("set YOUTUBE_INTEGRATION_TEST=1 to explicitly call YouTube")
	}
	t.Log("Planned search.list calls: <= 2; no retries or pagination")
	key := os.Getenv("YOUTUBE_API_KEY")
	if key == "" {
		t.Fatal("YOUTUBE_API_KEY is required")
	}
	source, err := client.NewYouTubeClient(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	music := &client.CachedMusicClient{Source: source, Cache: client.NewInMemorySearchCache(), TTL: time.Minute, Budget: client.NewSearchBudget(2), LiveEnabled: true}
	data, err := os.ReadFile("../model/testdata/analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := model.DecodeImageAnalysis(data)
	if err != nil {
		t.Fatal(err)
	}
	region := os.Getenv("YOUTUBE_REGION")
	if region == "" {
		region = "KR"
	}
	language := os.Getenv("YOUTUBE_RELEVANCE_LANGUAGE")
	if language == "" {
		language = "ko"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req := model.RecommendationRequest{Analysis: *analysis, Preferences: model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10}}
	result, err := service.NewRecommendationService(fixedIntegrationQueries{}, music, region, language, 2, 15*time.Second).Recommend(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tracks) == 0 {
		t.Fatal("no verified music candidates")
	}
	// Re-fetch every returned ID in one batch; no fixed track assertions.
	ids := make([]string, 0, len(result.Tracks))
	for _, v := range result.Tracks {
		ids = append(ids, v.VideoID)
	}
	verified, err := music.GetVideos(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]model.YouTubeVideo{}
	for _, v := range verified {
		found[v.VideoID] = v
	}
	for _, v := range result.Tracks {
		source, exists := found[v.VideoID]
		if !exists || source.Title != v.Title || source.ChannelTitle != v.ChannelTitle || source.DurationSeconds != v.DurationSeconds {
			t.Fatal("returned track not confirmed by YouTube")
		}
	}
	t.Logf("returned %d independently verified tracks", len(result.Tracks))
}
