package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"google.golang.org/api/googleapi"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

const YouTubeScope = "https://www.googleapis.com/auth/youtube"
const StateLifetime = 10 * time.Minute
const SessionLifetime = 24 * time.Hour

var (
	ErrAuthExpired   = errors.New("YouTube OAuth token expired")
	ErrConfiguration = errors.New("OAuth configuration unavailable")
	ErrStateInvalid  = errors.New("invalid OAuth state")
	ErrCodeMissing   = errors.New("OAuth code missing")
	ErrTokenExchange = errors.New("OAuth token exchange failed")
	ErrYouTubeAuth   = errors.New("YouTube authentication failed")
)

type Settings struct {
	ClientID, ClientSecret, RedirectURL, Scope string
	CookieSecure, ForceConsent                 bool
	Invalid                                    bool
}

type Channel struct {
	ID    *string `json:"channel_id"`
	Title *string `json:"channel_title"`
}
type Connection struct {
	Connected bool     `json:"connected"`
	YouTube   *Channel `json:"youtube,omitempty"`
}

// ChannelVerifier receives an OAuth HTTP client, never a YouTube public-search API key.
type ChannelVerifier interface {
	Verify(context.Context, *http.Client) (*Channel, error)
}
type YouTubeChannelVerifier struct{}

func (YouTubeChannelVerifier) Verify(ctx context.Context, httpClient *http.Client) (*Channel, error) {
	api, err := youtube.NewService(ctx, option.WithHTTPClient(httpClient), option.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return nil, err
	}
	response, err := api.Channels.List([]string{"snippet"}).Mine(true).MaxResults(1).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	channel := &Channel{}
	if len(response.Items) > 0 && response.Items[0] != nil {
		item := response.Items[0]
		channel.ID = &item.Id
		if item.Snippet != nil {
			channel.Title = &item.Snippet.Title
		}
	}
	return channel, nil
}

type sessionLock struct {
	gate  chan struct{}
	users int
}

type pendingFlow struct {
	expires                   time.Time
	verifier, previousSession string
}
type GoogleOAuthService struct {
	config       *oauth2.Config
	store        TokenStore
	verifier     ChannelVerifier
	settings     Settings
	mu           sync.Mutex
	pending      map[string]pendingFlow
	sessionLocks map[string]*sessionLock
}

func NewGoogleOAuthService(settings Settings, store TokenStore, verifier ChannelVerifier) *GoogleOAuthService {
	parsed, err := url.Parse(settings.RedirectURL)
	valid := err == nil && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Path == "/api/v1/auth/google/callback" && (parsed.Scheme == "https" || (parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")))
	s := &GoogleOAuthService{settings: settings, store: store, verifier: verifier, pending: make(map[string]pendingFlow), sessionLocks: make(map[string]*sessionLock)}
	if !settings.Invalid && settings.ClientID != "" && settings.ClientSecret != "" && settings.Scope == YouTubeScope && valid && store != nil && verifier != nil {
		s.config = &oauth2.Config{ClientID: settings.ClientID, ClientSecret: settings.ClientSecret, RedirectURL: settings.RedirectURL, Scopes: []string{YouTubeScope}, Endpoint: google.Endpoint}
	}
	return s
}
func (s *GoogleOAuthService) Configured() bool   { return s != nil && s.config != nil }
func (s *GoogleOAuthService) CookieSecure() bool { return s.settings.CookieSecure }
func RandomID() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}
func (s *GoogleOAuthService) Start(ctx context.Context, previousSession string) (string, string, error) {
	if !s.Configured() {
		return "", "", ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	state, err := RandomID()
	if err != nil {
		return "", "", err
	}
	verifier := oauth2.GenerateVerifier()
	s.mu.Lock()
	now := time.Now()
	for key, flow := range s.pending {
		if !now.Before(flow.expires) {
			delete(s.pending, key)
		}
	}
	// Bound pending-flow memory even if an anonymous client repeatedly starts OAuth.
	if len(s.pending) >= 4096 {
		s.mu.Unlock()
		return "", "", errors.New("OAuth flow capacity reached")
	}
	s.pending[state] = pendingFlow{now.Add(StateLifetime), verifier, previousSession}
	s.mu.Unlock()
	options := []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("include_granted_scopes", "true"), oauth2.S256ChallengeOption(verifier)}
	if s.settings.ForceConsent {
		options = append(options, oauth2.SetAuthURLParam("prompt", "consent"))
	}
	log.Print("OAuth flow started")
	return state, s.config.AuthCodeURL(state, options...), nil
}
func (s *GoogleOAuthService) consumeState(state, cookie string) (pendingFlow, error) {
	if len(state) != 43 || len(cookie) != 43 || subtle.ConstantTimeCompare([]byte(state), []byte(cookie)) != 1 {
		return pendingFlow{}, ErrStateInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, exists := s.pending[state]
	delete(s.pending, state)
	if !exists || !time.Now().Before(flow.expires) {
		return pendingFlow{}, ErrStateInvalid
	}
	return flow, nil
}
func (s *GoogleOAuthService) Cancel(state, cookie string) {
	if s.Configured() {
		_, _ = s.consumeState(state, cookie)
	}
}
func (s *GoogleOAuthService) Complete(ctx context.Context, state, cookie, code string) (string, *Connection, error) {
	if !s.Configured() {
		return "", nil, ErrConfiguration
	}
	flow, err := s.consumeState(state, cookie)
	if err != nil {
		return "", nil, err
	}
	if code == "" {
		return "", nil, ErrCodeMissing
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	token, err := s.config.Exchange(ctx, code, oauth2.VerifierOption(flow.verifier))
	// Never retain or log RetrieveError: its body can contain confidential data.
	if err != nil || token == nil || !token.Valid() || token.TokenType != "Bearer" && token.TokenType != "bearer" {
		log.Printf("OAuth token exchange failed reason=%s", safeErrorReason(err))
		return "", nil, ErrTokenExchange
	}
	channel, err := s.verifier.Verify(ctx, s.config.Client(ctx, token))
	if err != nil || channel == nil {
		log.Printf("OAuth YouTube verification failed reason=%s", safeErrorReason(err))
		return "", nil, ErrYouTubeAuth
	}
	// Reauthorization rotates the session. Preserve an old refresh token ONLY when
	// YouTube confirms the same channel; switching Google accounts must never mix tokens.
	if token.RefreshToken == "" && flow.previousSession != "" && channel.ID != nil {
		oldToken, getErr := s.store.Get(ctx, flow.previousSession)
		if getErr == nil && oldToken.RefreshToken != "" {
			oldChannel, verifyErr := s.verifier.Verify(ctx, s.config.Client(ctx, oldToken))
			if verifyErr == nil && oldChannel != nil && oldChannel.ID != nil && *oldChannel.ID == *channel.ID {
				token.RefreshToken = oldToken.RefreshToken
			}
		}
	}
	id, err := RandomID()
	if err != nil {
		return "", nil, err
	}
	if err := s.store.Save(ctx, id, token); err != nil {
		return "", nil, err
	}
	if flow.previousSession != "" {
		_ = s.store.Delete(context.WithoutCancel(ctx), flow.previousSession)
	}
	log.Print("OAuth callback and YouTube verification succeeded")
	return id, &Connection{Connected: true, YouTube: channel}, nil
}

// Serialize refreshes per session without sharing a canceled request context.
func (s *GoogleOAuthService) lockSession(ctx context.Context, id string) (func(), error) {
	s.mu.Lock()
	lock := s.sessionLocks[id]
	if lock == nil {
		lock = &sessionLock{gate: make(chan struct{}, 1)}
		lock.gate <- struct{}{}
		s.sessionLocks[id] = lock
	}
	lock.users++
	s.mu.Unlock()
	releaseRef := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		lock.users--
		if lock.users == 0 {
			delete(s.sessionLocks, id)
		}
	}
	select {
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	case <-lock.gate:
		return func() { lock.gate <- struct{}{}; releaseRef() }, nil
	}
}

type persistedTokenSource struct {
	source  oauth2.TokenSource
	store   TokenStore
	session string
	ctx     context.Context
}

func (s *persistedTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if err := s.store.Update(s.ctx, s.session, token); err != nil {
		return nil, err
	}
	return token, nil
}
func (s *GoogleOAuthService) Status(ctx context.Context, id string) (*Connection, error) {
	if !s.Configured() {
		return nil, ErrConfiguration
	}
	if id == "" {
		return &Connection{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	unlock, err := s.lockSession(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	token, err := s.store.Get(ctx, id)
	if errors.Is(err, ErrSessionNotFound) {
		return &Connection{}, nil
	}
	if err != nil {
		return nil, err
	}
	source := &persistedTokenSource{s.config.TokenSource(ctx, token), s.store, id, ctx}
	channel, err := s.verifier.Verify(ctx, oauth2.NewClient(ctx, source))
	if err != nil || channel == nil {
		log.Printf("OAuth status YouTube verification failed reason=%s", safeErrorReason(err))
		return nil, ErrYouTubeAuth
	}
	return &Connection{Connected: true, YouTube: channel}, nil
}
func (s *GoogleOAuthService) Disconnect(ctx context.Context, id string) error {
	if !s.Configured() {
		return ErrConfiguration
	}
	unlock, err := s.lockSession(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	// TODO: add explicit Google grant revocation separately from local disconnect.
	return s.store.Delete(ctx, id)
}

// Provider messages, request URLs and response bodies may contain secrets.
// Log only a fixed category or numeric HTTP status.
func safeErrorReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var tokenError *oauth2.RetrieveError
	if errors.As(err, &tokenError) && tokenError.Response != nil {
		return fmt.Sprintf("http_%d", tokenError.Response.StatusCode)
	}
	var apiError *googleapi.Error
	if errors.As(err, &apiError) {
		return fmt.Sprintf("http_%d", apiError.Code)
	}
	if err == nil {
		return "invalid_provider_response"
	}
	return "provider_or_transport_error"
}

// The callback owns this request-scoped client; handlers never receive tokens.
func (s *GoogleOAuthService) WithAuthenticatedClient(ctx context.Context, id string, operation func(*http.Client) error) error {
	if id == "" {
		return ErrSessionNotFound
	}
	if !s.Configured() {
		return ErrConfiguration
	}
	unlock, err := s.lockSession(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	token, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	source := &persistedTokenSource{s.config.TokenSource(ctx, token), s.store, id, ctx}
	guarded := &writeTokenSource{source: source, ctx: ctx}
	// Resolve refresh before creating any external resource.
	if _, err := guarded.Token(); err != nil {
		return err
	}
	return operation(oauth2.NewClient(ctx, guarded))
}

type writeTokenSource struct {
	source oauth2.TokenSource
	ctx    context.Context
}

func (s *writeTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.source.Token()
	if err != nil {
		if s.ctx.Err() != nil {
			return nil, s.ctx.Err()
		}
		if errors.Is(err, ErrSessionNotFound) {
			return nil, ErrSessionNotFound
		}
		log.Printf("OAuth write token refresh failed reason=%s", safeErrorReason(err))
		return nil, ErrAuthExpired
	}
	return token, nil
}
