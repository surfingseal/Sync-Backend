package model

import (
	"strings"
	"testing"
)

func TestDirectStructuredParsing(t *testing.T) {
	good := `{"analysis_summary":"warm","scene":{"description":"가을 밤 불꽃놀이 중인 공원"},"playlist":{"title":"Autumn Fireworks"},"tracks":[{"artist":"Artist","title":"Song","fit_score":0,"reason":"gentle","lyric_language":"ko"}]}`
	r, e := DecodeDirectRecommendation([]byte(good), 20)
	if e != nil || r.Tracks[0].GeminiRank != 1 || r.Tracks[0].FitScore != 0 {
		t.Fatal(r, e)
	}
	for _, bad := range []string{strings.Replace(good, `"fit_score":0,`, "", 1), strings.Replace(good, `"fit_score":0`, `"fit_score":2`, 1), strings.Replace(good, `"reason":"gentle"`, `"reason":"gentle","video_id":"invented"`, 1), strings.Replace(good, `"Artist"`, `"https://example.com"`, 1), "```json\n" + good + "\n```", good + good, `{"analysis_summary":"warm","scene":{"description":"가을 밤 불꽃놀이 중인 공원"},"playlist":{"title":"Autumn Fireworks"},"tracks":[]}`} {
		if _, e := DecodeDirectRecommendation([]byte(bad), 20); e == nil {
			t.Fatal("accepted invalid structure")
		}
	}
	blank := strings.Replace(good, `"Artist"`, `" "`, 1)
	if _, e := DecodeDirectRecommendation([]byte(blank), 20); e != nil {
		t.Fatal("blanks should be removed during normalization", e)
	}
}

func TestMVPStructuredSceneAndLanguage(t *testing.T) {
	good := `{"analysis_summary":"warm","scene":{"description":"가을 밤 불꽃놀이 중인 공원"},"playlist":{"title":"Autumn Fireworks"},"tracks":[{"artist":"Artist","title":"Song","fit_score":0.9,"reason":"fits","lyric_language":"ko_en"}]}`
	r, err := DecodeDirectRecommendation([]byte(good), 8)
	if err != nil || !r.Tracks[0].LyricLanguage.KoreanEligible() {
		t.Fatal(r, err)
	}
	for _, bad := range []string{strings.Replace(good, "가을 밤 불꽃놀이 중인 공원", "", 1), strings.Replace(good, "가을 밤 불꽃놀이 중인 공원", "English only", 1), strings.Replace(good, `,"lyric_language":"ko_en"`, "", 1)} {
		if _, err = DecodeDirectRecommendation([]byte(bad), 8); err == nil {
			t.Fatal("accepted incomplete schema")
		}
	}
	foreign := strings.Replace(good, `"lyric_language":"ko_en"`, `"lyric_language":"ja"`, 1)
	r, err = DecodeDirectRecommendation([]byte(foreign), 8)
	if err != nil || r.Tracks[0].LyricLanguage != LyricUnknown {
		t.Fatal(r, err)
	}
}
