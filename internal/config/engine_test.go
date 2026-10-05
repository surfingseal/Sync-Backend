package config

import "testing"

func TestEngineSelection(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  RecommendationEngine
	}{{"", LegacyEngine}, {"legacy", LegacyEngine}, {"gemini_direct", DirectEngine}} {
		v, e := ParseRecommendationEngine(tc.input)
		if e != nil || v != tc.want {
			t.Fatalf("%q %v %v", tc.input, v, e)
		}
	}
	if _, e := ParseRecommendationEngine("typo"); e == nil {
		t.Fatal("invalid engine silently accepted")
	}
	if ValidateServerEngine(LegacyEngine) != nil {
		t.Fatal("legacy rejected")
	}
	if ValidateServerEngine(DirectEngine) == nil {
		t.Fatal("direct enabled before readiness")
	}
}
