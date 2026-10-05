// retrieval-audit evaluates recorded pools without constructing any network client.
package main

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/service"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type input struct {
	ID       string              `json:"id"`
	Analysis model.ImageAnalysis `json:"analysis"`
}
type row struct {
	ID                           string   `json:"id"`
	Queries                      []string `json:"queries"`
	QueryMS                      float64  `json:"query_ms"`
	SimulatedSearches            int      `json:"simulated_searches"`
	SecondSkipped                bool     `json:"second_query_skipped"`
	Eligible                     int      `json:"eligible"`
	Vocal, Instrumental, Unknown int
	Response                     *model.RecommendationResponse `json:"response"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dataset := flag.String("dataset", "", "saved ImageAnalysis array")
	replay := flag.String("replay", "", "recorded candidate pools")
	out := flag.String("out", "", "new output file (never an old benchmark)")
	flag.Parse()
	if *dataset == "" || *replay == "" || *out == "" {
		return fmt.Errorf("dataset/replay/out required")
	}
	raw, err := os.ReadFile(*dataset)
	if err != nil {
		return err
	}
	var inputs []input
	if json.Unmarshal(raw, &inputs) != nil {
		return fmt.Errorf("invalid input")
	}
	source, err := client.NewReplayMusicClient(*replay)
	if err != nil {
		return err
	}
	rows := []row{}
	for _, in := range inputs {
		pools := []client.OfflinePool{}
		for _, p := range source.Pools {
			if strings.HasPrefix(p.Name, in.ID+"-") {
				pools = append(pools, p)
			}
		}
		music := &client.OfflineMusicClient{Mode: "replay", RecordedAt: source.RecordedAt, Pools: pools}
		trace := &service.RecommendationTrace{}
		prefs := model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10, VocalMode: "mixed"}
		svc := service.NewRecommendationService(client.DeterministicQueryBuilder{}, music, "KR", "ko", 2, time.Second, service.WithRanker(recommendation.RankV2))
		response, err := svc.Recommend(service.WithRecommendationTrace(context.Background(), trace), model.RecommendationRequest{Analysis: in.Analysis, Preferences: prefs})
		if err != nil {
			return fmt.Errorf("audit %s failed: %w", in.ID, err)
		}
		r := row{ID: in.ID, Queries: trace.SearchQueries, QueryMS: trace.QueryMS, SimulatedSearches: trace.Retrieval.ReplayRequests, SecondSkipped: trace.SecondQuerySkipped, Eligible: len(trace.Candidates), Response: response}
		for _, item := range trace.Ranked {
			switch recommendation.VocalKind(item.Video) {
			case "vocal":
				r.Vocal++
			case "instrumental":
				r.Instrumental++
			default:
				r.Unknown++
			}
		}
		rows = append(rows, r)
	}
	report := struct {
		Mode          string         `json:"mode"`
		ExternalCalls map[string]int `json:"external_calls"`
		Rows          []row          `json:"rows"`
	}{"offline recorded-pool simulation", map[string]int{"vertex": 0, "youtube_search_list": 0, "youtube_videos_list": 0, "oauth": 0, "playlist": 0}, rows}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(data, '\n'), 0600)
}
