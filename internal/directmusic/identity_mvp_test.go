package directmusic

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"example.com/sync/internal/model"
)

func TestMVPAuditReplay(t *testing.T) {
	data, err := os.ReadFile("testdata/mvp-identity-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Artist, Title string
		Accept        bool
		Video         model.YouTubeVideo
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Video.VideoID, func(t *testing.T) {
			before, _ := json.Marshal(tc.Video)
			e, why := CheckIdentity(model.DirectTrack{Artist: tc.Artist, Title: tc.Title}, tc.Video, DefaultConfig())
			if (why == "") != tc.Accept {
				t.Fatalf("accept=%v reason=%s evidence=%+v", tc.Accept, why, e)
			}
			after, _ := json.Marshal(tc.Video)
			if string(before) != string(after) {
				t.Fatal("raw metadata changed")
			}
		})
	}
}
func TestMVPFormattingAndFalsePositiveBoundaries(t *testing.T) {
	cases := []struct {
		name, artist, title, video string
		accept                     bool
	}{
		{"mv", "Colde", "와르르", "[MV] Colde - 와르르♥", true},
		{"parenthesized mv", "Colde", "와르르", "(MV) Colde - 와르르", true},
		{"m slash v quotes", "백예린", "Bye bye my blue", `Yerin Baek(백예린) "Bye bye my blue" M/V`, true},
		{"bilingual outer", "Colde", "와르르", "Colde (콜드) - 와르르♥", true},
		{"bilingual inner", "Crush", "어떻게 지내", "크러쉬(Crush) - '어떻게 지내'", true},
		{"official video", "Colde", "와르르", "Colde - 와르르 Official Video", true},
		{"official audio", "Colde", "와르르", "Colde - 와르르 Official Audio", true},
		{"music video", "Colde", "와르르", "Colde - 와르르 Music Video", true},
		{"nfkc", "Colde", "와르르", "Ｃｏｌｄｅ - “와르르”♥", true},
		{"different artist", "Colde", "와르르", "Other Artist - 와르르", false},
		{"artist substring", "Colde", "와르르", "SuperColde - 와르르", false},
		{"title substring", "Colde", "와르르", "Colde - 와르르 이야기", false},
		{"title mention", "Colde", "와르르", "Review of Colde 와르르", false},
		{"feat", "Colde", "와르르", "Other Artist feat. Colde - 와르르", false},
		{"solo feat", "Colde", "와르르", "Colde feat. Other Artist - 와르르", false},
		{"x collaboration", "Colde", "와르르", "Colde X Other Artist - 와르르", false},
		{"no delimiter x", "Colde", "와르르", "Colde X Other Artist 와르르", false},
		{"guest parentheses", "Colde", "와르르", "Other Artist (Colde) - 와르르", false},
		{"unproven parentheses", "Colde", "와르르", "Colde (Other Artist) - 와르르", false},
		{"internal word joining", "Colde", "와르르", "Colde - 와 르르", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := validVideo()
			v.Title = tc.video
			v.ChannelTitle = "Independent"
			_, why := CheckIdentity(model.DirectTrack{Artist: tc.artist, Title: tc.title}, v, DefaultConfig())
			if (why == "") != tc.accept {
				t.Fatalf("accept=%v reason=%s", tc.accept, why)
			}
		})
	}
	for _, marker := range []string{"cover", "live", "remix", "acoustic", "demo", "karaoke", "sped up", "slowed", "nightcore"} {
		t.Run(marker, func(t *testing.T) {
			v := validVideo()
			v.Title = "Beach House - Space Song (" + marker + ")"
			v.ViewCount = 1000000000
			if _, why := CheckIdentity(candidate(), v, DefaultConfig()); why == "" {
				t.Fatal("wrong version accepted")
			}
		})
	}
}
func TestMVPLocalizedRequiresReleaseEvidence(t *testing.T) {
	tr := model.DirectTrack{Artist: "Crush", Title: "어떻게 지내"}
	v := validVideo()
	v.Title = "[MV] Crush _ fall(어떻게 지내)"
	v.ChannelTitle = "Independent"
	v.LicensedContent = false
	if _, why := CheckIdentity(tr, v, DefaultConfig()); why == "" {
		t.Fatal("unsupported localized title accepted")
	}
	v.Description = "Crush - fall(어떻게 지내)\nCopyrights Artist Label"
	if _, why := CheckIdentity(tr, v, DefaultConfig()); why != "" {
		t.Fatal(why)
	}
	v.Title = "Crush - other song(어떻게 지내)"
	if _, why := CheckIdentity(tr, v, DefaultConfig()); why == "" {
		t.Fatal("contradicting provider title accepted")
	}
}

type mvpPool struct {
	videos []model.YouTubeVideo
	calls  int
	query  model.MusicSearchQuery
	ids    []string
}

func (f *mvpPool) SearchMusic(_ context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	f.calls++
	f.query = q
	out := []model.YouTubeSearchResult{}
	for _, v := range f.videos {
		out = append(out, model.YouTubeSearchResult{VideoID: v.VideoID})
	}
	return out, nil
}
func (f *mvpPool) GetVideos(_ context.Context, ids []string) ([]model.YouTubeVideo, error) {
	f.ids = append([]string{}, ids...)
	out := []model.YouTubeVideo{}
	// Reverse response order: resolver must preserve search order for final ties.
	for i := len(f.videos) - 1; i >= 0; i-- {
		for _, id := range ids {
			if f.videos[i].VideoID == id {
				out = append(out, f.videos[i])
			}
		}
	}
	return out, nil
}
func TestMVPResolverPrioritiesAndTopFive(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]model.YouTubeVideo)
		want string
	}{
		{"official before popularity", func(v []model.YouTubeVideo) { v[1].ChannelTitle = "Beach House - Topic" }, "b"},
		{"release before popularity", func(v []model.YouTubeVideo) { v[1].Description = "Beach House - Space Song\nCopyrights label" }, "b"},
		{"views only after identity", func(v []model.YouTubeVideo) { v[0].Title = "Other Artist - Space Song" }, "b"},
		{"popularity tie breaker", func(v []model.YouTubeVideo) { v[1].ViewCount = 2000 }, "b"},
		{"search rank final", func(v []model.YouTubeVideo) { v[1].ViewCount = 1000 }, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vs := []model.YouTubeVideo{validVideo(), validVideo()}
			for i := range vs {
				vs[i].ChannelTitle = "Independent"
				vs[i].LicensedContent = false
			}
			vs[0].VideoID = "a"
			vs[1].VideoID = "b"
			vs[0].ViewCount = 1000
			vs[1].ViewCount = 5
			tc.edit(vs)
			pool := &mvpPool{videos: vs}
			cache, _ := NewCache("")
			r := Resolver{Client: pool, Cache: cache, Config: DefaultConfig()}
			got, err := r.Resolve(context.Background(), candidate())
			if err != nil || got.Video == nil || got.Video.VideoID != tc.want || pool.calls != 1 || pool.query.Text != "Beach House Space Song" {
				t.Fatal(got, err, pool.calls, pool.query)
			}
		})
	}
	vs := []model.YouTubeVideo{}
	for _, id := range []string{"1", "2", "3", "4", "5", "6"} {
		v := validVideo()
		v.VideoID = id
		v.Title = "Other Artist - Space Song"
		vs = append(vs, v)
	}
	vs[5] = validVideo()
	vs[5].VideoID = "6"
	pool := &mvpPool{videos: vs}
	cache, _ := NewCache("")
	cfg := DefaultConfig()
	cfg.SearchResults = 10
	r := Resolver{Client: pool, Cache: cache, Config: cfg}
	got, err := r.Resolve(context.Background(), candidate())
	if err != nil || IsResolved(got.Status) || pool.query.MaxResults != 5 || !reflect.DeepEqual(pool.ids, []string{"1", "2", "3", "4", "5"}) || r.Stats.FallbackCalls != 0 {
		t.Fatal(got, err, pool.query, pool.ids)
	}
}
