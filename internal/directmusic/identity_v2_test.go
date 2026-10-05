package directmusic

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
)

func TestMetadataLabelsAndVersionMarkers(t *testing.T) {
	for _, s := range []string{"[MV] ADOY - Wonder", "(MV) ADOY - Wonder", "ADOY - Wonder (Official Audio)", "ADOY - Wonder Official Music Video"} {
		if NormalizeVideoTitle(s) != "ADOY - Wonder" {
			t.Fatal(s, NormalizeVideoTitle(s))
		}
	}
	for _, s := range []string{"Remix", "Live", "Cover", "Acoustic", "Demo", "Version", "Edit"} {
		v := "ADOY - Wonder (" + s + ")"
		if !strings.Contains(NormalizeVideoTitle(v), s) {
			t.Fatal("identity marker removed")
		}
	}
	v := validVideo()
	v.Title = "[MV] Beach House - Space Song"
	if _, why := CheckIdentity(candidate(), v, DefaultConfig()); why != "" {
		t.Fatal(why)
	}
	v.Title = "Some Artist - Beach House Space Song Cover"
	if _, why := CheckIdentity(candidate(), v, DefaultConfig()); why == "" {
		t.Fatal("foreign artist accepted")
	}
}
func TestCollaborativeCredits(t *testing.T) {
	credits := ArtistCredits("Stan Getz, João Gilberto & Astrud Gilberto")
	if !reflect.DeepEqual(credits, []string{"stan getz", "joao gilberto", "astrud gilberto"}) {
		t.Fatal(credits)
	}
	c := model.DirectTrack{Artist: "Stan Getz, João Gilberto & Astrud Gilberto", Title: "Corcovado (Quiet Nights of Quiet Stars)"}
	v := validVideo()
	v.Title = "Stan Getz feat. Astrud Gilberto - Corcovado (Quiet Nights of Quiet Stars) (Official Video)"
	v.ChannelTitle = "Verve Records"
	e, why := CheckIdentity(c, v, DefaultConfig())
	if why != "" || e.ArtistMatch != "collaborative_main_and_credit_overlap" {
		t.Fatal(e, why)
	}
	v.LicensedContent = false
	if _, why = CheckIdentity(c, v, DefaultConfig()); why == "" {
		t.Fatal("relaxed credit without release evidence")
	}
	v.LicensedContent = true
	v.Title = "Joao Gilberto & Astrud Gilberto - Corcovado (Quiet Nights of Quiet Stars)"
	if _, why = CheckIdentity(c, v, DefaultConfig()); why == "" {
		t.Fatal("missing main credit")
	}
}
func TestForeignCollaborativeCreditDescriptionCannotOverrideBoundary(t *testing.T) {
	c := model.DirectTrack{Artist: "Iron & Wine", Title: "Naked as We Came"}
	v := validVideo()
	v.Title = "Some Artist - Naked as We Came"
	v.ChannelTitle = "Some Artist - Topic"
	v.Description = "Originally written by Iron & Wine"
	v.LicensedContent = true
	if _, why := CheckIdentity(c, v, DefaultConfig()); why == "" {
		t.Fatal("description mention overrode foreign artist boundary")
	}
}

func TestLocalizedExplicitAliasOnly(t *testing.T) {
	c := model.DirectTrack{Artist: "Colde", Title: "WA-R-R"}
	v := validVideo()
	v.Title = "Colde - 와르르♥"
	v.ChannelTitle = "Colde - Topic"
	v.ChannelID = "channel"
	v.VideoID = "release"
	if _, why := CheckIdentity(c, v, DefaultConfig()); why == "" {
		t.Fatal("inferred cross-script alias")
	}
	aliases := []TitleAlias{{Artist: "Colde", CanonicalTitle: "WA-R-R", ProviderTitle: "와르르♥", VideoID: "release", ChannelID: "channel", EvidenceURL: "https://provider.example/release", EvidenceText: "explicit release title equivalence"}}
	e, why := CheckIdentityWithAliases(c, v, DefaultConfig(), aliases)
	if why != "" || e.TitleMatch != "explicit_provider_release_alias" {
		t.Fatal(e, why)
	}
	v.VideoID = "unrelated"
	if _, why = CheckIdentityWithAliases(c, v, DefaultConfig(), aliases); why == "" {
		t.Fatal("unbound alias")
	}
}
func TestOfficialitySeparateFromIdentity(t *testing.T) {
	v := validVideo()
	for _, tc := range []struct {
		channel  string
		licensed bool
		want     string
	}{{"Beach House - Topic", false, "topic"}, {"BeachHouseVEVO", false, "vevo"}, {"Independent", true, "licensed"}, {"Beach House", false, "official_artist"}, {"Independent", false, "ordinary_channel"}, {"", false, "unknown"}} {
		v.ChannelTitle = tc.channel
		v.LicensedContent = tc.licensed
		got, _ := ClassifyOfficiality(candidate(), v)
		if got != tc.want {
			t.Fatal(got, tc)
		}
	}
	v.ChannelTitle = "Independent"
	v.LicensedContent = false
	e, why := CheckIdentity(candidate(), v, DefaultConfig())
	if why != "" || e.Officiality != "ordinary_channel" || !e.Playable || ResolvedStatus(e) != "RESOLVED_ACCEPTABLE" {
		t.Fatal(e, why)
	}
	a, _ := json.Marshal(e)
	b, _ := json.Marshal(e)
	if string(a) != string(b) {
		t.Fatal("nondeterministic decision")
	}
}

type fallbackClient struct {
	queries     []string
	videosCalls int
	err         error
}

func (f *fallbackClient) SearchMusic(_ context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	f.queries = append(f.queries, q.Text)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.queries) == 1 {
		return nil, nil
	}
	return []model.YouTubeSearchResult{{VideoID: "id"}}, nil
}
func (f *fallbackClient) GetVideos(_ context.Context, _ []string) ([]model.YouTubeVideo, error) {
	f.videosCalls++
	return []model.YouTubeVideo{validVideo()}, nil
}
func TestControlledFallbackAndAPIFailure(t *testing.T) {
	cache, _ := NewCache("")
	f := &fallbackClient{}
	r := Resolver{Client: f, Cache: cache, Config: DefaultConfig()}
	out, e := r.Resolve(context.Background(), candidate())
	if e != nil || !IsResolved(out.Status) || len(f.queries) != 2 || f.queries[1] != "beach house space song" || r.Stats.PrimaryCalls != 1 || r.Stats.FallbackCalls != 1 || f.videosCalls != 1 {
		t.Fatal(out, e, f.queries, r.Stats)
	}
	cache, _ = NewCache("")
	f = &fallbackClient{err: client.ErrMusicSearch}
	r = Resolver{Client: f, Cache: cache, Config: DefaultConfig()}
	out, e = r.Resolve(context.Background(), candidate())
	if e == nil || out.Status != "API_FAILURE" || len(f.queries) != 1 {
		t.Fatal(out, e)
	}
	if cacheKey("a", "b", DefaultConfig()) == legacyCacheKey("a", "b", DefaultConfig()) {
		t.Fatal("negative cache version collision")
	}
}

func TestUnrelatedVersionIsIdentityFailure(t *testing.T) {
	v := validVideo()
	v.Title = "Other Song (Live)"
	v.ChannelTitle = "Other Artist - Topic"
	if _, why := CheckIdentity(candidate(), v, DefaultConfig()); why != "TITLE_MISMATCH" {
		t.Fatal(why)
	}
}
