package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordedRegressionCorpus(t *testing.T) {
	root := t.TempDir()
	for version, relative := range map[string]string{"v1": "artifacts/gemini-direct-v1-20261004T152933Z/per-photo", "v2": "artifacts/gemini-direct-resolver-v2-20261004T155634Z/live-run/per-photo"} {
		for _, photo := range []string{"still-life", "night-city"} {
			data, err := os.ReadFile(filepath.Join("../../internal/directapi/testdata/regression", version, photo, "resolver-results.json"))
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, relative, photo)
			if err = os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "resolver-results.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Chdir(root)
	r, e := recordedRegression()
	if e != nil || r.StructuralFailures != 0 || r.Winners != 40 || r.KnownFailures != 4 {
		t.Fatalf("%+v %v", r, e)
	}
}
