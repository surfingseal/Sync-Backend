// Package directbench measures technical readiness, never subjective music quality.
package directbench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

type Image struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Enabled    bool   `json:"enabled"`
	Category   string `json:"category,omitempty"`
	Provenance string `json:"provenance,omitempty"`
}
type Dataset struct {
	Images     []Image `json:"images"`
	Enabled    int     `json:"enabled_entries"`
	Duplicates int     `json:"duplicates_removed"`
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

func LoadManifest(path string) (Dataset, error) {
	var d Dataset
	d.Images = []Image{}
	f, err := os.Open(path)
	if err != nil {
		return d, fmt.Errorf("cannot read image manifest")
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1024*1024))
	dec.DisallowUnknownFields()
	var rows []Image
	if dec.Decode(&rows) != nil || dec.Decode(new(any)) != io.EOF || len(rows) == 0 || len(rows) > 500 {
		return d, fmt.Errorf("invalid image manifest")
	}
	ids := map[string]bool{}
	hashes := map[string]bool{}
	for _, r := range rows {
		if !idPattern.MatchString(r.ID) || ids[r.ID] || r.Path == "" {
			return d, fmt.Errorf("invalid or duplicate image ID")
		}
		ids[r.ID] = true
		if !r.Enabled {
			continue
		}
		d.Enabled++
		if !filepath.IsAbs(r.Path) {
			r.Path = filepath.Join(filepath.Dir(path), r.Path)
		}
		b, e := ReadImage(r.Path)
		if e != nil {
			return d, e
		}
		sum := sha256.Sum256(b)
		actual := hex.EncodeToString(sum[:])
		if len(r.SHA256) != 64 || r.SHA256 != actual {
			return d, fmt.Errorf("image content hash mismatch for %s", r.ID)
		}
		if hashes[actual] {
			d.Duplicates++
			continue
		}
		hashes[actual] = true
		d.Images = append(d.Images, r)
	}
	if len(d.Images) == 0 {
		return d, fmt.Errorf("manifest has no enabled unique images")
	}
	return d, nil
}
func ReadImage(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read benchmark image")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 10*1024*1024+1))
	if err != nil || len(b) == 0 || len(b) > 10*1024*1024 {
		return nil, fmt.Errorf("invalid benchmark image size")
	}
	return b, nil
}
