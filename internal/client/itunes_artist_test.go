package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestArtworkExplicitBilingualEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, requested, provider, title, evidence string
		want                                       bool
	}{
		{"normalized", "Tom Misch", "ＴＯＭ MISCH", "Movie", "", true},
		{"provider bilingual", "백예린", "Yerin Baek(백예린)", "Bye bye my blue", "", true},
		{"verified bilingual", "백예린", "Yerin Baek", "Bye bye my blue", `Yerin Baek(백예린) "Bye bye my blue" M/V`, true},
		{"no bridge", "백예린", "Yerin Baek", "Bye bye my blue", "", false},
		{"wrong title bridge", "백예린", "Yerin Baek", "Bye bye my blue", `Yerin Baek(백예린) "Other Song" M/V`, false},
		{"same title wrong artist", "백예린", "Other Artist", "Bye bye my blue", `Yerin Baek(백예린) "Bye bye my blue" M/V`, false},
		{"collaboration", "백예린", "Yerin Baek", "Bye bye my blue", `Yerin Baek X Other(백예린) "Bye bye my blue" M/V`, false},
		{"substring", "백예린", "Yerin Baek", "Bye bye my blue", `Yerin Baek(백예린) "Bye bye my blue Again" M/V`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := artworkArtistCompatible(tc.requested, tc.provider, tc.title, tc.evidence); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestArtworkUSDefaultAndEvidenceScopedCache(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("country") != "US" {
			t.Error("expected default US")
		}
		item := itunesItem("Yerin Baek", "Bye bye my blue")
		item["collectionName"] = "Bye bye my blue - Single"
		json.NewEncoder(w).Encode(map[string]any{"resultCount": 1, "results": []any{item}})
	}))
	defer server.Close()
	a := NewAlbumArtworkResolver(server.Client(), server.URL, "")
	ctx := context.Background()
	if got, err := a.Resolve(ctx, "백예린", "Bye bye my blue"); got != nil || err != nil {
		t.Fatalf("unsupported match %v %v", got, err)
	}
	evidence := `Yerin Baek(백예린) "Bye bye my blue" M/V`
	got, err := a.ResolveWithArtistEvidence(ctx, "백예린", "Bye bye my blue", evidence)
	if err != nil || got == nil {
		t.Fatalf("explicit match %v %v", got, err)
	}
	if got, err := a.ResolveWithArtistEvidence(ctx, "백예린", "Bye bye my blue", evidence); got == nil || err != nil || calls != 2 {
		t.Fatal("evidence cache miss")
	}
	if got, _ := a.Resolve(ctx, "백예린", "Bye bye my blue"); got != nil || calls != 2 {
		t.Fatal("cross evidence cache contamination")
	}
}

func TestArtworkUSCatalogAndVersionRejections(t *testing.T) {
	for _, tc := range []struct {
		name, artist, title, album string
		want                       bool
	}{
		{"US exact", "Tom Misch", "Movie", "Geography", true},
		{"wrong performer compilation", "Other", "Movie", "Various Artists", false},
		{"tribute album", "Tom Misch", "Movie", "A Tribute to Tom Misch", false},
		{"cover artist", "Cover Band", "Movie", "Geography", false},
		{"cover", "Tom Misch", "Movie (Cover)", "Geography", false},
		{"karaoke", "Tom Misch", "Movie", "Karaoke Versions", false},
		{"instrumental cover", "Tom Misch", "Movie", "Instrumental Covers", false},
		{"remix", "Tom Misch", "Movie (Remix)", "Geography", false},
		{"live album", "Tom Misch", "Movie", "Live Sessions", false},
		{"substring", "Tom Misch", "Movie Night", "Geography", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				items := []any{}
				if r.URL.Query().Get("country") == "US" {
					item := itunesItem(tc.artist, tc.title)
					item["collectionName"] = tc.album
					items = append(items, item)
				}
				json.NewEncoder(w).Encode(map[string]any{"resultCount": len(items), "results": items})
			}))
			defer server.Close()
			a := NewAlbumArtworkResolver(server.Client(), server.URL, "")
			got, err := a.Resolve(context.Background(), "Tom Misch", "Movie")
			if err != nil || (got != nil) != tc.want {
				t.Fatalf("got %v err %v", got, err)
			}
			kr := NewAlbumArtworkResolver(server.Client(), server.URL, "KR")
			if got, err := kr.Resolve(context.Background(), "Tom Misch", "Movie"); got != nil || err != nil {
				t.Fatal("expected KR empty")
			}
		})
	}
}

// Recorded US provider responses, replayed locally: external calls are zero.
func TestArtworkSavedUSProviderEvidence(t *testing.T) {
	var fixtures []struct {
		Artist, Title, Evidence string
		Response                json.RawMessage
		Want                    bool
	}
	data, err := os.ReadFile("testdata/itunes_us_artwork_audit.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Artist, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(f.Response) }))
			defer server.Close()
			a := NewAlbumArtworkResolver(server.Client(), server.URL, "")
			got, err := a.ResolveWithArtistEvidence(context.Background(), f.Artist, f.Title, f.Evidence)
			if err != nil || (got != nil) != f.Want {
				t.Fatalf("got %+v err %v want=%v", got, err, f.Want)
			}
		})
	}
}
