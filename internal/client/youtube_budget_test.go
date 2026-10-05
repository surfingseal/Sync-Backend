package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestHTTPBudgetIncludesFallbackRetriesAndConcurrency(t *testing.T) {
	for _, split := range [][2]int{{8, 1}, {5, 4}} {
		var calls atomic.Int64
		b := NewSearchBudget(9)
		tr := BudgetTransport{Budget: b, Base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		for i := 0; i < split[0]+split[1]; i++ {
			r, _ := http.NewRequestWithContext(context.Background(), "GET", "https://example.test/youtube/v3/search", nil)
			resp, e := tr.RoundTrip(r)
			if e != nil {
				t.Fatal(e)
			}
			resp.Body.Close()
		}
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, _ := http.NewRequest("GET", "https://example.test/youtube/v3/search", nil)
				if _, e := tr.RoundTrip(r); e == nil {
					t.Error("10th call admitted")
				}
			}()
		}
		wg.Wait()
		if calls.Load() != 9 || b.Used() != 9 {
			t.Fatal(calls.Load(), b.Used())
		}
		r, _ := http.NewRequest("GET", "https://example.test/youtube/v3/videos", nil)
		if _, e := tr.RoundTrip(r); e != nil {
			t.Fatal("metadata spent search budget")
		}
	}
	b := NewSearchBudget(9)
	var called atomic.Int64
	tr := BudgetTransport{Budget: b, Base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := http.NewRequest("GET", "https://example.test/youtube/v3/search", nil)
			tr.RoundTrip(r)
		}()
	}
	wg.Wait()
	if called.Load() != 9 {
		t.Fatal(called.Load())
	}
}
