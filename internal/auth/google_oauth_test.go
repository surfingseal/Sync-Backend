package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

type unusedVerifier struct{}

func (unusedVerifier) Verify(context.Context, *http.Client) (*Channel, error) { return &Channel{}, nil }
func testOAuth() *GoogleOAuthService {
	return NewGoogleOAuthService(Settings{ClientID: "test", ClientSecret: "test", RedirectURL: "http://localhost:8080/api/v1/auth/google/callback", Scope: YouTubeScope}, NewInMemoryTokenStore(), unusedVerifier{})
}
func TestExpiredAndReplayedState(t *testing.T) {
	s := testOAuth()
	state, _, err := s.Start(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	flow := s.pending[state]
	flow.expires = time.Now().Add(-time.Second)
	s.pending[state] = flow
	if _, err := s.consumeState(state, state); !errors.Is(err, ErrStateInvalid) {
		t.Fatal("expired state accepted")
	}
	state, _, _ = s.Start(context.Background(), "")
	if _, err := s.consumeState(state, state); err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumeState(state, state); !errors.Is(err, ErrStateInvalid) {
		t.Fatal("replayed state accepted")
	}
}
func TestConfigurationAndCancellation(t *testing.T) {
	for _, settings := range []Settings{{}, {ClientID: "test", ClientSecret: "test", RedirectURL: "http://attacker.example/api/v1/auth/google/callback", Scope: YouTubeScope}, {ClientID: "test", ClientSecret: "test", RedirectURL: "http://localhost:8080/api/v1/auth/google/callback/", Scope: YouTubeScope}, {ClientID: "test", ClientSecret: "test", RedirectURL: "http://localhost:8080/api/v1/auth/google/callback", Scope: "openid"}} {
		if NewGoogleOAuthService(settings, NewInMemoryTokenStore(), unusedVerifier{}).Configured() {
			t.Fatal("invalid configuration accepted")
		}
	}
	s := testOAuth()
	_, location, _ := s.Start(context.Background(), "")
	u, _ := url.Parse(location)
	if u.Query().Has("prompt") {
		t.Fatal("consent forced when disabled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.Start(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatal("Start ignored cancellation")
	}
}
