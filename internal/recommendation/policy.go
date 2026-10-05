package recommendation

import (
	"example.com/sync/internal/model"
	"math"
)

// Experimental V2 policy is centralized, not learned or a quality guarantee.
// Unknown SDK zero/omitted likes/comments stay nil, not a claim of zero reactions.
var policy = struct {
	Weights                                                                      struct{ Mood, Popularity, Trust, Engagement, Diversity float64 }
	RelativeViewsWeight, RelativeLikesWeight, CommentBonus, MaxDiscoveryFraction float64
	MinimumFinalScore                                                            float64
	QualityGate                                                                  bool
	ViewAnchors, LikeAnchors                                                     []anchor
	StrongViewPenalty, ModerateViewPenalty, SmallViewPenalty                     float64
	StrongLikePenalty, MildLikePenalty                                           float64
}{
	Weights:             struct{ Mood, Popularity, Trust, Engagement, Diversity float64 }{.45, .25, .15, .05, .10},
	RelativeViewsWeight: .4, RelativeLikesWeight: .4, CommentBonus: .02, MaxDiscoveryFraction: .2,
	QualityGate:       true,
	MinimumFinalScore: .30,
	ViewAnchors:       []anchor{{0, 0}, {1000, .08}, {10000, .30}, {100000, .60}, {1000000, .85}, {10000000, 1}},
	LikeAnchors:       []anchor{{0, 0}, {5, .08}, {20, .25}, {100, .60}, {1000, .85}, {10000, 1}},
	StrongViewPenalty: .12, ModerateViewPenalty: .06, SmallViewPenalty: .015,
	StrongLikePenalty: .05, MildLikePenalty: .025,
}

type anchor struct {
	Count uint64
	Score float64
}

func absolute(count uint64, points []anchor) float64 {
	x := math.Log10(float64(count) + 1)
	for i := 1; i < len(points); i++ {
		if count <= points[i].Count {
			lo, hi := points[i-1], points[i]
			part := (x - math.Log10(float64(lo.Count)+1)) / (math.Log10(float64(hi.Count)+1) - math.Log10(float64(lo.Count)+1))
			return clamp(lo.Score + part*(hi.Score-lo.Score))
		}
	}
	return 1
}
func absoluteViews(n uint64) float64 { return absolute(n, policy.ViewAnchors) }
func absoluteLikes(n uint64) float64 { return absolute(n, policy.LikeAnchors) }
func popularityBucket(n uint64) string {
	if n < policy.ViewAnchors[2].Count {
		return "discovery"
	}
	if n < policy.ViewAnchors[3].Count {
		return "medium"
	}
	return "established"
}
func FailsQualityGate(v model.YouTubeVideo) bool {
	return policy.QualityGate && v.ViewCount < policy.ViewAnchors[1].Count && v.LikeCount != nil && *v.LikeCount < policy.LikeAnchors[1].Count && trustScore(v) == 0
}
func weakReactionPenalty(v model.YouTubeVideo) float64 {
	p := 0.0
	switch {
	case v.ViewCount < policy.ViewAnchors[1].Count:
		p = policy.StrongViewPenalty
	case v.ViewCount < policy.ViewAnchors[2].Count:
		p = policy.ModerateViewPenalty
	case v.ViewCount < policy.ViewAnchors[3].Count:
		p = policy.SmallViewPenalty
	}
	if v.LikeCount != nil {
		if *v.LikeCount < policy.LikeAnchors[1].Count {
			p += policy.StrongLikePenalty
		} else if *v.LikeCount < policy.LikeAnchors[2].Count {
			p += policy.MildLikePenalty
		}
	}
	return p
}
