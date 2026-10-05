package recommendation

import (
	"example.com/sync/internal/model"
	"math"
	"sort"
	"strings"
)

// Weights are centralized. Metadata keyword matches are proxies for atmosphere;
// neither audio understanding nor official ownership is inferred here.
var weights = struct{ Mood, Popularity, Trust, Engagement, Diversity float64 }{.45, .20, .15, .10, .10}

type Candidate struct {
	Video      model.YouTubeVideo
	SearchRank int
}
type Scores struct{ Mood, Popularity, Trust, Engagement, Diversity, Final, RelativeViews, AbsoluteViews, AbsoluteLikes, WeakPenalty float64 }
type RankedCandidate struct {
	Video  model.YouTubeVideo
	Track  model.RecommendedTrack
	Scores Scores
	Bucket string
}

func clamp(x float64) float64   { return math.Max(0, math.Min(1, x)) }
func rounded(x float64) float64 { return math.Round(clamp(x)*10000) / 10000 }
func channel(v model.YouTubeVideo) string {
	if v.ChannelID != "" {
		return v.ChannelID
	}
	return normalize(v.ChannelTitle)
}
func matchFraction(text string, values []string) float64 {
	if len(values) == 0 {
		return 0
	}
	n := 0
	for _, value := range values {
		if phrase(text, value) {
			n++
		}
	}
	return float64(n) / float64(len(values))
}
func languageMatch(v model.YouTubeVideo, p model.MusicPreferences) bool {
	lang := v.DefaultAudioLanguage
	if lang == "" {
		lang = v.DefaultLanguage
	}
	lang = strings.ToLower(strings.Split(lang, "-")[0])
	for _, want := range p.Languages {
		if lang == want {
			return true
		}
	}
	return false
}
func moodScore(v model.YouTubeVideo, rank int, a model.ImageAnalysis, p model.MusicPreferences) float64 {
	text := v.Title + " " + v.Description + " " + v.ChannelTitle
	genreWeight := .30
	if len(p.PreferredGenres) == 0 {
		genreWeight = .40
	}
	score := .15*clamp(1-float64(rank)/20) + .40*matchFraction(text, a.Mood.Tags) + genreWeight*matchFraction(text, a.MusicProfile.Genres) + .10*matchFraction(text, p.PreferredGenres)
	if languageMatch(v, p) {
		score += .05
	}
	return clamp(score)
}
func trustScore(v model.YouTubeVideo) float64 {
	score := 0.0
	if v.LicensedContent {
		score += .5
	}
	title := strings.ToLower(strings.TrimSpace(v.ChannelTitle))
	if strings.HasSuffix(title, " - topic") || strings.HasSuffix(title, "vevo") {
		score += .25
	}
	for _, p := range []string{"official audio", "official video", "official mv", "music video"} {
		if phrase(v.Title+" "+v.Description, p) {
			score += .25
			break
		}
	}
	return clamp(score)
}
func logNormalize(value uint64, low, high float64) float64 {
	if value == 0 {
		return 0
	}
	if high == low {
		return .5
	}
	return clamp((math.Log10(float64(value)+1) - low) / (high - low))
}

// Rank deduplicates IDs, uses pool-relative log statistics, then selects with
// channel and relative popularity diversity. Channel max 2 is relaxed only when
// no unselected candidate from another channel remains. No lower-view cutoff.
func Rank(input []Candidate, a model.ImageAnalysis, p model.MusicPreferences) []RankedCandidate {
	return rank(input, a, p, false)
}
func RankV2(input []Candidate, a model.ImageAnalysis, p model.MusicPreferences) []RankedCandidate {
	return rank(input, a, p, true)
}
func rank(input []Candidate, a model.ImageAnalysis, p model.MusicPreferences, v2 bool, audits ...*RankingAudit) []RankedCandidate {
	// Sorting first makes duplicate resolution and ties independent of map/input order.
	candidates := append([]Candidate(nil), input...)
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Video.VideoID != candidates[j].Video.VideoID {
			return candidates[i].Video.VideoID < candidates[j].Video.VideoID
		}
		return candidates[i].SearchRank < candidates[j].SearchRank
	})
	unique := []Candidate{}
	seen := map[string]bool{}
	for _, c := range candidates {
		if c.Video.VideoID == "" || seen[c.Video.VideoID] || ExclusionReason(c.Video) != "" {
			continue
		}
		seen[c.Video.VideoID] = true
		if v2 && FailsQualityGate(c.Video) {
			continue
		}
		unique = append(unique, c)
	}
	if len(unique) == 0 {
		return []RankedCandidate{}
	}
	lowViews, highViews := math.Inf(1), 0.0
	lowLikes, highLikes := math.Inf(1), 0.0
	byViews := append([]Candidate(nil), unique...)
	sort.Slice(byViews, func(i, j int) bool {
		if byViews[i].Video.ViewCount != byViews[j].Video.ViewCount {
			return byViews[i].Video.ViewCount < byViews[j].Video.ViewCount
		}
		return byViews[i].Video.VideoID < byViews[j].Video.VideoID
	})
	buckets := map[string]string{}
	for i, c := range byViews {
		bucket := "high"
		if float64(i)/float64(len(byViews)) < .2 {
			bucket = "discovery"
		} else if float64(i)/float64(len(byViews)) < .5 {
			bucket = "medium"
		}
		if v2 {
			bucket = popularityBucket(c.Video.ViewCount)
		}
		buckets[c.Video.VideoID] = bucket
	}
	for _, c := range unique {
		views := math.Log10(float64(c.Video.ViewCount) + 1)
		lowViews = math.Min(lowViews, views)
		highViews = math.Max(highViews, views)
		if c.Video.LikeCount != nil {
			likes := math.Log10(float64(*c.Video.LikeCount) + 1)
			lowLikes = math.Min(lowLikes, likes)
			highLikes = math.Max(highLikes, likes)
		}
	}
	pool := make([]RankedCandidate, 0, len(unique))
	for _, c := range unique {
		v := c.Video
		engagement := .5 // neutral for absent/undisclosed likes; never a filter
		if v.LikeCount != nil {
			engagement = logNormalize(*v.LikeCount, lowLikes, highLikes)
		}
		scores := Scores{Mood: moodScore(v, c.SearchRank, a, p), Popularity: logNormalize(v.ViewCount, lowViews, highViews), Trust: trustScore(v), Engagement: engagement}
		if v2 {
			scores.RelativeViews = scores.Popularity
			scores.AbsoluteViews = absoluteViews(v.ViewCount)
			scores.Popularity = policy.RelativeViewsWeight*scores.RelativeViews + (1-policy.RelativeViewsWeight)*scores.AbsoluteViews
			scores.AbsoluteLikes = .5
			if v.LikeCount != nil {
				scores.AbsoluteLikes = absoluteLikes(*v.LikeCount)
			}
			scores.Engagement = policy.RelativeLikesWeight*engagement + (1-policy.RelativeLikesWeight)*scores.AbsoluteLikes
			// Comments are weak supplementary evidence, never a gate.
			if v.CommentCount != nil && *v.CommentCount > 0 {
				scores.Engagement = clamp(scores.Engagement + policy.CommentBonus*clamp(math.Log10(float64(*v.CommentCount)+1)/3))
			}
			scores.WeakPenalty = weakReactionPenalty(v)
		}
		reasons := []string{"retrieved through mood-based YouTube search"}
		text := v.Title + " " + v.Description + " " + v.ChannelTitle
		for _, tag := range a.Mood.Tags {
			if phrase(text, tag) {
				reasons = append(reasons, "matches "+tag+" mood keyword")
			}
		}
		for _, genre := range append(append([]string{}, a.MusicProfile.Genres...), p.PreferredGenres...) {
			if phrase(text, genre) {
				reasons = append(reasons, "matches "+genre+" genre keyword")
			}
		}
		if v2 && scores.AbsoluteViews >= .6 {
			reasons = append(reasons, "absolute audience reaction signal")
		}
		if !v2 && scores.Popularity >= .7 {
			reasons = append(reasons, "popular within this candidate pool")
		}
		if v.LicensedContent {
			reasons = append(reasons, "licensed music content")
		}
		if languageMatch(v, p) {
			reasons = append(reasons, "language preference matches YouTube metadata")
		}
		pool = append(pool, RankedCandidate{Video: v, Scores: scores, Bucket: buckets[v.VideoID], Track: model.RecommendedTrack{VideoID: v.VideoID, Title: v.Title, ChannelTitle: v.ChannelTitle, ThumbnailURL: v.ThumbnailURL, DurationSeconds: v.DurationSeconds, MatchReasons: reasons, YouTubeURL: "https://www.youtube.com/watch?v=" + v.VideoID}})
	}
	result := []RankedCandidate{}
	channels := map[string]int{}
	selectedBuckets := map[string]int{}
	for len(pool) > 0 && len(result) < max(p.Count, p.CandidatePoolLimit) {
		limited := false
		for _, c := range pool {
			if channels[channel(c.Video)] < 2 {
				limited = true
				break
			}
		}
		best := -1
		bestScore := -1.0
		for i := range pool {
			c := &pool[i]
			if v2 && c.Bucket == "discovery" && selectedBuckets["discovery"] >= max(1, int(math.Ceil(float64(p.Count)*policy.MaxDiscoveryFraction))) {
				continue
			}
			repeats := channels[channel(c.Video)]
			if limited && repeats >= 2 {
				continue
			}
			diversity := .8 / float64(repeats+1)
			// Soft balance, not a quota: first medium/discovery with good mood can
			// receive a small extra bonus, but weak mood is not promoted just for rarity.
			if selectedBuckets[c.Bucket] == 0 && c.Scores.Mood >= .4 {
				diversity += .2
			}
			c.Scores.Diversity = clamp(diversity)
			activeWeights := weights
			if v2 {
				activeWeights = policy.Weights
			}
			score := activeWeights.Mood*c.Scores.Mood + activeWeights.Popularity*c.Scores.Popularity + activeWeights.Trust*c.Scores.Trust + activeWeights.Engagement*c.Scores.Engagement + activeWeights.Diversity*c.Scores.Diversity
			score -= c.Scores.WeakPenalty
			if phrase(c.Video.Title, "remix") {
				score -= .03
			}
			if c.Video.DurationSeconds > 600 {
				score -= .02
			}
			c.Scores.Final = rounded(score)
			if len(audits) > 0 && audits[0] != nil && len(result) == 0 {
				snapshot := *c
				snapshot.Track.MatchScore = snapshot.Scores.Final
				audits[0].InitialScores = append(audits[0].InitialScores, snapshot)
			}
			if v2 && c.Scores.Final < policy.MinimumFinalScore {
				continue
			}
			if c.Scores.Final > bestScore || (c.Scores.Final == bestScore && (best < 0 || c.Video.VideoID < pool[best].Video.VideoID)) {
				best = i
				bestScore = c.Scores.Final
			}
		}
		if best < 0 {
			break
		} // partial results instead of filling with weak discovery
		item := pool[best]
		item.Track.MatchScore = item.Scores.Final
		if channels[channel(item.Video)] == 0 {
			item.Track.MatchReasons = append(item.Track.MatchReasons, "channel diversity")
		}
		result = append(result, item)
		channels[channel(item.Video)]++
		selectedBuckets[item.Bucket]++
		pool = append(pool[:best], pool[best+1:]...)
	}
	// Presentation by final marginal score; selection constraints remain intact.
	sort.Slice(result, func(i, j int) bool {
		if result[i].Track.MatchScore != result[j].Track.MatchScore {
			return result[i].Track.MatchScore > result[j].Track.MatchScore
		}
		return result[i].Video.VideoID < result[j].Video.VideoID
	})
	return result
}

// RankingAudit is read-only observation of the first marginal evaluation. Later
// diversity effects may reduce scores; no weight/gate/selection changes are made.
type RankingAudit struct {
	InitialScores []RankedCandidate
	MinimumScore  float64
}

func RankV2WithAudit(input []Candidate, a model.ImageAnalysis, p model.MusicPreferences) ([]RankedCandidate, RankingAudit) {
	audit := RankingAudit{MinimumScore: policy.MinimumFinalScore}
	result := rank(input, a, p, true, &audit)
	return result, audit
}
