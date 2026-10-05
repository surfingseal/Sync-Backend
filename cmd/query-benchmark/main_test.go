package main

import (
	"context"
	"errors"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"testing"
	"time"
)

type fakeGenerator struct {
	calls     int
	fail      bool
	wait      bool
	hashInput model.ImageAnalysis
}

func (f *fakeGenerator) GenerateMusicQueries(ctx context.Context, a model.ImageAnalysis, p model.MusicPreferences, n int) ([]string, error) {
	f.calls++
	f.hashInput = a
	if f.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.fail {
		return nil, client.ErrInvalidAIResponse
	}
	return []string{"korean calm acoustic music", "english nostalgic indie music"}, nil
}
func TestBenchmarkNeverFallsBack(t *testing.T) {
	f := &fakeGenerator{fail: true}
	r := measure(context.Background(), f, input{ID: "photo"}, model.MusicPreferences{Languages: []string{"ko", "en"}}, "gemini", "6s", time.Second)
	if f.calls != 1 || r.Success || r.FallbackUsed || len(r.Queries) != 0 || r.ErrorClass != "invalid_response" {
		t.Fatal(r)
	}
}
func TestBenchmarkContextAndIdentity(t *testing.T) {
	f := &fakeGenerator{wait: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := measure(ctx, f, input{ID: "photo"}, model.MusicPreferences{}, "gemini", "6s", time.Second)
	if r.ErrorClass != "canceled" || f.calls != 1 {
		t.Fatal(r)
	}
	timeout := measure(context.Background(), f, input{ID: "photo"}, model.MusicPreferences{}, "gemini", "6s", time.Millisecond)
	if !timeout.Timeout || timeout.ErrorClass != "timeout" {
		t.Fatal(timeout)
	}
	g := &fakeGenerator{}
	p := model.MusicPreferences{Languages: []string{"ko", "en"}}
	a := measure(context.Background(), g, input{ID: "photo"}, p, "gemini", "6s", time.Second)
	b := measure(context.Background(), g, input{ID: "photo"}, p, "deterministic", "local", time.Second)
	if !a.Success || a.QueryCount != 2 || a.InputSHA256 != b.InputSHA256 || !a.SchemaSuccess {
		t.Fatal(a, b)
	}
	if classify(errors.New("must not leak raw error")) != "provider_error" {
		t.Fatal("raw error leaked")
	}
}
