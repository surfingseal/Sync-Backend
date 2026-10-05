package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func readJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("benchmark input file unavailable")
	}
	if json.Unmarshal(raw, target) != nil {
		return fmt.Errorf("invalid benchmark input JSON")
	}
	return nil
}
func lines(raw []byte) [][]byte      { return bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) }
func key(p, id string, n int) string { return fmt.Sprintf("%s/%s/%d", p, id, n) }
func splitIDs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
