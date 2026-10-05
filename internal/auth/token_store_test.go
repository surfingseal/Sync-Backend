package auth

import (
	"context"
	"errors"
	"golang.org/x/oauth2"
	"testing"
	"time"
)

func TestTokenStore(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryTokenStore()
	original := &oauth2.Token{AccessToken: "access", RefreshToken: "refresh"}
	if err := s.Save(ctx, "session", original); err != nil {
		t.Fatal(err)
	}
	original.AccessToken = "mutated"
	token, _ := s.Get(ctx, "session")
	if token.AccessToken != "access" {
		t.Fatal("Save did not copy")
	}
	token.AccessToken = "mutated-get"
	token, _ = s.Get(ctx, "session")
	if token.AccessToken != "access" {
		t.Fatal("Get did not copy")
	}
	if err := s.Update(ctx, "session", &oauth2.Token{AccessToken: "new"}); err != nil {
		t.Fatal(err)
	}
	token, _ = s.Get(ctx, "session")
	if token.RefreshToken != "refresh" {
		t.Fatal("refresh token lost")
	}
	if err := s.Delete(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "session"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("token survived logout")
	}
	if err := s.Update(ctx, "session", original); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("refresh resurrected session")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Save(canceled, "session", original); !errors.Is(err, context.Canceled) {
		t.Fatal("Save ignored cancellation")
	}
	if _, err := s.Get(canceled, "session"); !errors.Is(err, context.Canceled) {
		t.Fatal("Get ignored cancellation")
	}
	if err := s.Delete(canceled, "session"); !errors.Is(err, context.Canceled) {
		t.Fatal("Delete ignored cancellation")
	}
}

func TestTokenStoreSessionExpiry(t *testing.T) {
	s := NewInMemoryTokenStore()
	ctx := context.Background()
	if err := s.Save(ctx, "expired", &oauth2.Token{AccessToken: "test-access"}); err != nil {
		t.Fatal(err)
	}
	item := s.tokens["expired"]
	item.expires = time.Now().Add(-time.Second)
	s.tokens["expired"] = item
	if _, err := s.Get(ctx, "expired"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("expired session remained available")
	}
	if err := s.Update(ctx, "expired", &oauth2.Token{AccessToken: "new"}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("expired session refreshed")
	}
}
