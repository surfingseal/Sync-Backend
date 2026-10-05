package main

import (
	"encoding/json"
	"example.com/sync/internal/directmusic"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordedV1Replay(t *testing.T) {
	root := t.TempDir()
	for _, photo := range []string{"still-life", "night-city"} {
		data, err := os.ReadFile(filepath.Join("../../internal/directapi/testdata/regression/v1", photo, "resolver-results.json"))
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, v1EvidenceRoot, "per-photo", photo)
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "resolver-results.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	out := t.TempDir()
	if err := offlineReplay(out, directmusic.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "accepted-v1-regression.json"))
	if err != nil {
		t.Fatal(err)
	}
	var regression struct {
		Total     int `json:"total"`
		Preserved int `json:"same_id_accepted"`
		Rejected  int `json:"same_id_rejected"`
	}
	// v2.2 rejects the unrequested Feist album version and Yukika's unknown
	// free-form suffix. Known MV labels remain accepted; no arbitrary prefix match.
	if json.Unmarshal(b, &regression) != nil || regression.Total != 20 || regression.Preserved != 18 || regression.Rejected != 2 {
		t.Fatal(string(b))
	}
	b, err = os.ReadFile(filepath.Join(out, "offline-v2-replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	var replay struct {
		Calls     int              `json:"external_api_calls"`
		Decisions []replayDecision `json:"decisions"`
	}
	if json.Unmarshal(b, &replay) != nil || replay.Calls != 0 {
		t.Fatal(string(b))
	}
	adoyFound := false
	for _, d := range replay.Decisions {
		if d.Artist == "ADOY" && d.V2Status == "TEXT_IDENTITY_MATCH_REQUIRES_METADATA" {
			adoyFound = true
		}
		if d.Artist == "Colde" && d.V2Status == "TEXT_IDENTITY_MATCH_REQUIRES_METADATA" {
			t.Fatal("localized relation was invented")
		}
	}
	if !adoyFound {
		t.Fatal("recorded MV prefix failure not fixed")
	}
}
