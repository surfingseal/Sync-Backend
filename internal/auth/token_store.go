package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

var ErrSessionNotFound = errors.New("OAuth session not found")

// Update only replaces an existing session: a concurrent refresh cannot undo logout.
type TokenStore interface {
	Save(context.Context, string, *oauth2.Token) error
	Get(context.Context, string) (*oauth2.Token, error)
	Update(context.Context, string, *oauth2.Token) error
	Delete(context.Context, string) error
}

type storedToken struct {
	token   oauth2.Token
	expires time.Time
}

type InMemoryTokenStore struct {
	mu     sync.RWMutex
	tokens map[string]storedToken
}

func NewInMemoryTokenStore() *InMemoryTokenStore {
	return &InMemoryTokenStore{tokens: make(map[string]storedToken)}
}
func (s *InMemoryTokenStore) save(ctx context.Context, id string, token *oauth2.Token, existingOnly bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" || token == nil || token.AccessToken == "" {
		return errors.New("invalid OAuth session token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, stored := range s.tokens {
		if !now.Before(stored.expires) {
			delete(s.tokens, key)
		}
	}
	old, exists := s.tokens[id]
	if existingOnly && !exists {
		return ErrSessionNotFound
	}
	copy := *token
	if copy.RefreshToken == "" {
		copy.RefreshToken = old.token.RefreshToken
	}
	expires := old.expires
	if !exists {
		expires = now.Add(SessionLifetime)
	}
	s.tokens[id] = storedToken{token: copy, expires: expires}
	return nil
}
func (s *InMemoryTokenStore) Save(ctx context.Context, id string, token *oauth2.Token) error {
	return s.save(ctx, id, token, false)
}
func (s *InMemoryTokenStore) Update(ctx context.Context, id string, token *oauth2.Token) error {
	return s.save(ctx, id, token, true)
}
func (s *InMemoryTokenStore) Get(ctx context.Context, id string) (*oauth2.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	token, exists := s.tokens[id]
	if !exists || !time.Now().Before(token.expires) {
		return nil, ErrSessionNotFound
	}
	copy := token.token
	return &copy, nil
}
func (s *InMemoryTokenStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, id)
	return nil
}
