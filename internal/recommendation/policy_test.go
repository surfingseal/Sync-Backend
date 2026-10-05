package recommendation

import (
	"math"
	"reflect"
	"testing"
)

func likes(c Candidate, n uint64) Candidate { c.Video.LikeCount = &n; return c }
func TestV2AbsoluteSignalsAndMood(t *testing.T) {
	a, p := fixture()
	weak := likes(candidate("weak", "calm warm acoustic", 2000), 10)
	strong := likes(candidate("strong", "calm warm acoustic", 2000000), 20000)
	r := RankV2([]Candidate{weak, strong}, a, p)
	if r[0].Video.VideoID != "strong" || r[0].Scores.AbsoluteViews <= r[1].Scores.AbsoluteViews || r[0].Scores.AbsoluteLikes <= r[1].Scores.AbsoluteLikes {
		t.Fatal(r)
	}
	wrong := likes(candidate("wrong", "loud dance", 100000000), 1000000)
	match := likes(candidate("match", "calm warm acoustic", 50000), 500)
	r = RankV2([]Candidate{wrong, match}, a, p)
	if r[0].Video.VideoID != "match" {
		t.Fatal("popularity overrides strong mood", r)
	}
	if absoluteViews(math.MaxUint64) != 1 {
		t.Fatal("overflow")
	}
}
func TestV2GateDiscoveryPartialAndDeterminism(t *testing.T) {
	a, p := fixture()
	tiny := likes(candidate("tiny", "calm warm acoustic", 100), 1)
	if !FailsQualityGate(tiny.Video) || len(RankV2([]Candidate{tiny}, a, p)) != 0 {
		t.Fatal("composite gate missing")
	}
	discovery := likes(candidate("discovery", "calm warm acoustic", 5000), 12)
	discovery.Video.LicensedContent = true
	r := RankV2([]Candidate{tiny, discovery}, a, p)
	if len(r) != 1 || r[0].Video.VideoID != "discovery" {
		t.Fatal("trusted discovery lost", r)
	}
	pool := []Candidate{discovery, likes(candidate("d2", "calm acoustic", 500), 2), likes(candidate("d3", "calm acoustic", 2000), 10), likes(candidate("d4", "calm acoustic", 7000), 30)}
	pool[1].Video.LicensedContent = true
	r = RankV2(pool, a, p)
	if len(r) > 2 {
		t.Fatal("all-discovery pool fills top10", r)
	}
	if !reflect.DeepEqual(r, RankV2(pool, a, p)) {
		t.Fatal("not deterministic")
	}
	// Unknown likes are not asserted to be zero by the compound gate.
	unknown := candidate("unknown", "calm acoustic", 100)
	if FailsQualityGate(unknown.Video) {
		t.Fatal("unknown likes deleted")
	}
}
func TestV2ChannelAndContentRegression(t *testing.T) {
	a, p := fixture()
	p.Count = 5
	pool := []Candidate{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		c := likes(candidate(id, "calm warm acoustic official audio", 100000), 1000)
		if id < "d" {
			c.Video.ChannelID = "same"
		}
		pool = append(pool, c)
	}
	ai := candidate("ai", "AI cover calm acoustic", 10000000)
	bad := candidate("bad", "calm acoustic slowed reverb", 10000000)
	pool = append(pool, ai, bad, pool[0])
	r := RankV2(pool, a, p)
	same := 0
	for _, c := range r {
		if c.Video.VideoID == "ai" || c.Video.VideoID == "bad" {
			t.Fatal("content regression")
		}
		if c.Video.ChannelID == "same" {
			same++
		}
	}
	if len(r) != 5 || same > 2 {
		t.Fatal(r)
	}
}

func TestV2MinimumCompositeQuality(t *testing.T) {
	a, p := fixture()
	c := candidate("weak-trusted", "unknown song", 4)
	c.Video.LicensedContent = true
	if len(RankV2([]Candidate{c}, a, p)) != 0 {
		t.Fatal("weak composite score filled result")
	}
}
