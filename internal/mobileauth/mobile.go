// Package mobileauth bridges browser OAuth to an opaque Sync session. It never
// receives Google tokens. One coordinator owns the three bounded store views so
// consuming a handoff and issuing a session is one atomic operation.
package mobileauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"example.com/sync/internal/auth"
)

const (
	TransactionTTL  = 5 * time.Minute
	HandoffTTL      = 120 * time.Second
	SessionTTL      = auth.SessionLifetime
	DefaultCapacity = 4096
)

var (
	ErrUnavailable = errors.New("mobile auth unavailable")
	ErrInvalid     = errors.New("invalid or expired mobile authorization")
	ErrChallenge   = errors.New("invalid S256 challenge")
	ErrCapacity    = errors.New("mobile auth capacity reached")
	ErrSession     = errors.New("invalid mobile session")
)

type Settings struct{ BaseURL, PackageName, SigningSHA256, OAuthRedirectURL string }

var packagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
var fingerprintPattern = regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`)

func (s Settings) Valid() bool {
	u, e := url.Parse(s.BaseURL)
	r, re := url.Parse(s.OAuthRedirectURL)
	return e == nil && re == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/") && u.Scheme == r.Scheme && u.Host == r.Host && packagePattern.MatchString(s.PackageName) && fingerprintPattern.MatchString(strings.ToUpper(s.SigningSHA256))
}

// Provider is the existing Web OAuth service, including its own Google PKCE and
// refresh preservation. BindBrowser attaches the actual browser's old session.
type Provider interface {
	Configured() bool
	Start(context.Context, string) (string, string, error)
	BindBrowser(context.Context, string, string) error
	Complete(context.Context, string, string, string) (string, *auth.Connection, error)
	Cancel(string, string)
	SessionExists(context.Context, string) (bool, error)
}
type Transaction struct {
	ID, OAuthState, Challenge, ChallengeMethod, FlowType, AuthorizationURL, Phase string
	CreatedAt, ExpiresAt                                                          time.Time
}
type Handoff struct {
	CodeHash                                      [32]byte
	TransactionID, CredentialReference, Challenge string
	CreatedAt, ExpiresAt                          time.Time
	Consumed                                      bool
}
type Session struct {
	TokenHash                                   [32]byte
	ID, CredentialReference                     string
	CreatedAt, ExpiresAt, LastUsedAt, RevokedAt time.Time
}

// These views are replaceable with durable storage. A future implementation must
// preserve Exchange's transaction+handoff+session atomicity, not compose Get/Save.
type MobileAuthTransactionStore interface {
	Transaction(string) (Transaction, bool)
}
type MobileHandoffStore interface {
	Handoff([32]byte) (Handoff, bool)
}
type MobileSessionStore interface {
	Session([32]byte) (Session, bool)
	Revoke([32]byte)
}
type Store interface {
	MobileAuthTransactionStore
	MobileHandoffStore
	MobileSessionStore
	Create(Transaction) error
	Begin(string) (Transaction, error)
	Recognizes(string) bool
	ClaimCallback(string) (Transaction, error)
	Fail(string)
	Complete(string, Handoff) error
	Exchange(context.Context, [32]byte, string, Session, func(string) (bool, error)) (Session, error)
	RevokeReference(string)
}

func Challenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
func ValidVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return false
		}
	}
	return true
}
func validOpaque(s string) bool {
	b, e := base64.RawURLEncoding.DecodeString(s)
	return e == nil && len(s) == 43 && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}

type StartResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	ExpiresIn        int    `json:"expires_in"`
}
type ExchangeResponse struct {
	SessionToken string `json:"session_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}
type Service struct {
	settings Settings
	provider Provider
	store    Store
	now      func() time.Time
}

func New(settings Settings, provider Provider, store Store) *Service {
	return &Service{settings: settings, provider: provider, store: store, now: time.Now}
}
func (s *Service) Configured() bool {
	return s != nil && s.settings.Valid() && s.provider != nil && s.provider.Configured() && s.store != nil
}
func (s *Service) Start(ctx context.Context, challenge, method string) (*StartResponse, error) {
	if !s.Configured() {
		return nil, ErrUnavailable
	}
	if method != "S256" || !validOpaque(challenge) {
		return nil, ErrChallenge
	}
	id, e := auth.RandomID()
	if e != nil {
		return nil, e
	}
	state, location, e := s.provider.Start(ctx, "")
	if e != nil {
		return nil, e
	}
	now := s.now()
	t := Transaction{ID: id, OAuthState: state, Challenge: challenge, ChallengeMethod: method, FlowType: "mobile", AuthorizationURL: location, Phase: "created", CreatedAt: now, ExpiresAt: now.Add(TransactionTTL)}
	if e = s.store.Create(t); e != nil {
		s.provider.Cancel(state, state)
		return nil, e
	}
	return &StartResponse{strings.TrimRight(s.settings.BaseURL, "/") + "/api/v1/auth/google/mobile/authorize?transaction=" + url.QueryEscape(id), int(TransactionTTL.Seconds())}, nil
}
func (s *Service) Begin(ctx context.Context, id, previousSession string) (Transaction, error) {
	if !s.Configured() {
		return Transaction{}, ErrUnavailable
	}
	if !validOpaque(id) {
		return Transaction{}, ErrInvalid
	}
	t, e := s.store.Begin(id)
	if e != nil {
		return t, e
	}
	if e = s.provider.BindBrowser(ctx, t.OAuthState, previousSession); e != nil {
		s.store.Fail(t.ID)
		return Transaction{}, e
	}
	return t, nil
}
func (s *Service) IsMobile(state string) bool {
	return s != nil && s.store != nil && state != "" && s.store.Recognizes(state)
}
func (s *Service) Complete(ctx context.Context, state, cookie, code string) (string, error) {
	if !s.Configured() {
		return "", ErrUnavailable
	}
	t, e := s.store.ClaimCallback(state)
	if e != nil {
		return "", e
	}
	ref, _, e := s.provider.Complete(ctx, state, cookie, code)
	if e != nil {
		s.store.Fail(t.ID)
		return "", e
	}
	raw, e := auth.RandomID()
	if e != nil {
		s.store.Fail(t.ID)
		return "", e
	}
	now := s.now()
	h := Handoff{CodeHash: sha256.Sum256([]byte(raw)), TransactionID: t.ID, CredentialReference: ref, Challenge: t.Challenge, CreatedAt: now, ExpiresAt: now.Add(HandoffTTL)}
	if e = s.store.Complete(t.ID, h); e != nil {
		s.store.Fail(t.ID)
		return "", e
	}
	return strings.TrimRight(s.settings.BaseURL, "/") + "/auth/android/complete?code=" + url.QueryEscape(raw), nil
}
func (s *Service) Cancel(state, cookie string) {
	if s == nil {
		return
	}
	if t, e := s.store.ClaimCallback(state); e == nil {
		s.store.Fail(t.ID)
	}
	s.provider.Cancel(state, cookie)
}
func (s *Service) Exchange(ctx context.Context, code, verifier string) (*ExchangeResponse, error) {
	if !s.Configured() {
		return nil, ErrUnavailable
	}
	if !validOpaque(code) || !ValidVerifier(verifier) {
		return nil, ErrInvalid
	}
	raw, e := auth.RandomID()
	if e != nil {
		return nil, e
	}
	id, e := auth.RandomID()
	if e != nil {
		return nil, e
	}
	now := s.now()
	session := Session{TokenHash: sha256.Sum256([]byte(raw)), ID: id, CreatedAt: now, ExpiresAt: now.Add(SessionTTL)}
	_, e = s.store.Exchange(ctx, sha256.Sum256([]byte(code)), verifier, session, func(ref string) (bool, error) { return s.provider.SessionExists(ctx, ref) })
	if e != nil {
		return nil, e
	}
	return &ExchangeResponse{raw, "Bearer", int(SessionTTL.Seconds())}, nil
}
func (s *Service) Resolve(ctx context.Context, token string) (string, error) {
	if !s.Configured() {
		return "", ErrSession
	}
	if !validOpaque(token) {
		return "", ErrSession
	}
	session, ok := s.store.Session(sha256.Sum256([]byte(token)))
	if !ok {
		return "", ErrSession
	}
	ok, e := s.provider.SessionExists(ctx, session.CredentialReference)
	if e != nil {
		return "", e
	}
	if !ok {
		return "", ErrSession
	}
	return session.CredentialReference, nil
}
func (s *Service) Revoke(token string) {
	if s != nil && s.store != nil {
		s.store.Revoke(sha256.Sum256([]byte(token)))
	}
}
func (s *Service) RevokeReference(ref string) {
	if s != nil && s.store != nil {
		s.store.RevokeReference(ref)
	}
}
func (s *Service) AssetLinks() any {
	return []any{map[string]any{"relation": []string{"delegate_permission/common.handle_all_urls"}, "target": map[string]any{"namespace": "android_app", "package_name": s.settings.PackageName, "sha256_cert_fingerprints": []string{strings.ToUpper(s.settings.SigningSHA256)}}}}
}
