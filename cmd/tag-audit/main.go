// tag-audit is deliberately independent from the API server and recommendation DI.
package main

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	live := flag.Bool("live", false, "explicitly enable real Last.fm requests")
	fresh := flag.Bool("fresh", false, "bypass all local cached responses")
	out := flag.String("out", "artifacts/lastfm-tag-audit-"+time.Now().UTC().Format("20060102T150405Z"), "new output directory")
	cache := flag.String("cache-dir", "artifacts/lastfm-cache", "public response cache (24h)")
	max := flag.Int("max-enrich", 40, "bounded useful tags, 31..60")
	limit := flag.Int("sample-limit", 10, "tracks per sampled tag, 10..20")
	interval := flag.Int("interval-ms", 300, "delay between tags, at least 200ms")
	offline := flag.String("offline", "", "existing audit JSON; no external calls")
	review := flag.String("review", "", "explicit metadata-semantic review JSON")
	registry := flag.String("registry", "", "optional registry destination (not loaded by server)")
	flag.Parse()
	if *max < 31 || *max > 60 || *limit < 10 || *limit > 20 || *interval < 200 {
		return fmt.Errorf("invalid audit bounds")
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("output directory already exists; choose a new --out")
	}
	var a *lastfm.Audit
	var runErr error
	if *offline != "" {
		b, err := os.ReadFile(*offline)
		if err != nil {
			return err
		}
		a = &lastfm.Audit{}
		if err = json.Unmarshal(b, a); err != nil {
			return err
		}
	} else {
		if !*live {
			return fmt.Errorf("use --live for controlled external audit, or --offline for saved review")
		}
		key := os.Getenv("LASTFM_API_KEY")
		if key == "" {
			return fmt.Errorf("LASTFM_API_KEY is required for live audit")
		}
		c := lastfm.New(key)
		c.CacheDir = *cache
		c.Fresh = *fresh
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		a, runErr = lastfm.Run(ctx, c, lastfm.AuditConfig{MaxEnrich: *max, SampleLimit: *limit, Fresh: *fresh, IntervalMS: *interval}, *out)
	}
	if *review != "" {
		if *offline == "" {
			return fmt.Errorf("reviews require --offline; preserve first live run unchanged")
		}
		b, err := os.ReadFile(*review)
		if err != nil {
			return err
		}
		var reviews map[string]lastfm.Review
		if err = json.Unmarshal(b, &reviews); err != nil {
			return err
		}
		if err = lastfm.ApplyReviews(a, reviews); err != nil {
			return err
		}
	}
	if err := lastfm.Write(a, *out); err != nil {
		return err
	}
	if *registry != "" {
		if err := os.MkdirAll(filepath.Dir(*registry), 0755); err != nil {
			return err
		}
		b, err := json.MarshalIndent(lastfm.GenerateRegistry(a.Tags), "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(*registry, append(b, '\n'), 0644); err != nil {
			return err
		}
	}
	fmt.Printf("audit saved: %s; complete=%t; elapsed=%.2fs\n", *out, a.Complete, a.ElapsedMS/1000)
	return runErr
}
