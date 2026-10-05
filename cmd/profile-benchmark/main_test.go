package main

import (
	"encoding/json"
	"example.com/sync/internal/model"
	"math"
	"os"
	"testing"
)

func TestConsistencyHarness(t *testing.T) {
	raw, err := os.ReadFile("../../internal/model/testdata/enhanced-analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	var analyses []model.ImageAnalysis
	for i := 0; i < 3; i++ {
		a, err := model.DecodeEnhancedImageAnalysis(raw)
		if err != nil {
			t.Fatal(err)
		}
		analyses = append(analyses, *a)
	}
	r, err := compare("test", analyses)
	if err != nil || r.PairCount != 3 || r.PrimaryMoodAgreement != 1 || r.SecondaryMoodJaccard != 1 || r.GenreTop3Jaccard != 1 || r.GenreFamilyAgreement != 1 || r.TempoAgreement != 1 || r.MusicEnergyMAE != 0 || r.ConfidenceVariation["genre"].StdDev != 0 {
		t.Fatal(r, err)
	}
	analyses[1].MusicProfile.Energy += .3
	r, err = compare("test", analyses)
	if err != nil || math.Abs(r.MusicEnergyMAE-.2) > 1e-9 {
		t.Fatal(r, err)
	}
	if r, err := compare("test", analyses[:2]); err != nil || r.PairCount != 1 {
		t.Fatal("two-repeat comparison failed")
	}
	analyses[0].MusicProfile.GenreCandidates[0].Score = 2
	if _, err := compare("test", analyses); err == nil {
		t.Fatal("invalid enhanced analysis accepted")
	}
	if jaccard(nil, nil) != 1 || jaccard([]string{"a"}, []string{"b"}) != 0 {
		t.Fatal("jaccard")
	}
	// No network or provider SDK invocation: schema/model serialization only.
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
}
