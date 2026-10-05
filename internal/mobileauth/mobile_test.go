package mobileauth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/sync/internal/auth"
)

type fakeProvider struct {
	valid                bool
	calls                atomic.Int32
	bindErr, completeErr error
}

func (f *fakeProvider) Configured() bool { return true }
func (f *fakeProvider) Start(ctx context.Context, _ string) (string, string, error) {
	if e := ctx.Err(); e != nil {
		return "", "", e
	}
	state, _ := auth.RandomID()
	return state, "https://accounts.example/authorize?state=" + state, nil
}
func (f *fakeProvider) BindBrowser(ctx context.Context, _, _ string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	return f.bindErr
}
func (f *fakeProvider) Complete(ctx context.Context, state, cookie, code string) (string, *auth.Connection, error) {
	f.calls.Add(1)
	if e := ctx.Err(); e != nil {
		return "", nil, e
	}
	if state != cookie {
		return "", nil, auth.ErrStateInvalid
	}
	if code == "" {
		return "", nil, auth.ErrCodeMissing
	}
	if f.completeErr != nil {
		return "", nil, f.completeErr
	}
	return "credential-ref", &auth.Connection{Connected: true}, nil
}
func (f *fakeProvider) Cancel(string, string) {}
func (f *fakeProvider) SessionExists(ctx context.Context, _ string) (bool, error) {
	if e := ctx.Err(); e != nil {
		return false, e
	}
	return f.valid, nil
}
func settings() Settings {
	return Settings{BaseURL: "https://sync.example", PackageName: "org.test.sync", SigningSHA256: strings.TrimSuffix(strings.Repeat("AB:", 32), ":"), OAuthRedirectURL: "https://sync.example/api/v1/auth/google/callback"}
}
func setup(t *testing.T) (*Service, *MemoryStore, *fakeProvider, *time.Time) {
	t.Helper()
	clock := time.Now()
	m := NewMemoryStore(16)
	m.now = func() time.Time { return clock }
	p := &fakeProvider{valid: true}
	s := New(settings(), p, m)
	s.now = m.now
	return s, m, p, &clock
}
func handoff(t *testing.T, s *Service, verifier string) string {
	t.Helper()
	ctx := context.Background()
	res, e := s.Start(ctx, Challenge(verifier), "S256")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(res.AuthorizationURL)
	tx, e := s.Begin(ctx, u.Query().Get("transaction"), "")
	if e != nil {
		t.Fatal(e)
	}
	if !s.IsMobile(tx.OAuthState) {
		t.Fatal("not mobile")
	}
	location, e := s.Complete(ctx, tx.OAuthState, tx.OAuthState, "fake-code")
	if e != nil {
		t.Fatal(e)
	}
	u, _ = url.Parse(location)
	if len(u.Query()) != 1 {
		t.Fatal("redirect contains extra fields")
	}
	return u.Query().Get("code")
}
func TestS256AndHashOnlyStorage(t *testing.T) {
	s, m, p, _ := setup(t)
	v := strings.Repeat("a", 43)
	code := handoff(t, s, v)
	r, e := s.Exchange(context.Background(), code, v)
	if e != nil {
		t.Fatal(e)
	}
	if r.TokenType != "Bearer" || r.ExpiresIn != 86400 {
		t.Fatal(r)
	}
	ref, e := s.Resolve(context.Background(), r.SessionToken)
	if e != nil || ref != "credential-ref" {
		t.Fatal(ref, e)
	}
	if len(m.handoffs) != 1 || len(m.sessions) != 1 || p.calls.Load() != 1 {
		t.Fatal("unexpected calls or storage")
	}
	if _, ok := m.handoffs[sha256.Sum256([]byte(code))]; !ok {
		t.Fatal("handoff not hashed")
	}
	if _, ok := m.sessions[sha256.Sum256([]byte(r.SessionToken))]; !ok {
		t.Fatal("session not hashed")
	}
	if _, e = s.Exchange(context.Background(), code, v); !errors.Is(e, ErrInvalid) {
		t.Fatal("replay allowed", e)
	}
}
func TestRFC7636Vector(t *testing.T) {
	if Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal("S256 mismatch")
	}
}
func TestChallengeValidation(t *testing.T) {
	s, _, _, _ := setup(t)
	for _, tc := range []struct{ v, m string }{{Challenge(strings.Repeat("a", 43)), "plain"}, {"", "S256"}, {"invalid", "S256"}, {strings.Repeat("=", 43), "S256"}} {
		if _, e := s.Start(context.Background(), tc.v, tc.m); !errors.Is(e, ErrChallenge) {
			t.Fatal(tc, e)
		}
	}
}
func TestBadVerifierDoesNotIssueOrConsume(t *testing.T) {
	s, m, _, _ := setup(t)
	v := strings.Repeat("a", 43)
	code := handoff(t, s, v)
	for _, bad := range []string{"", strings.Repeat("b", 43), strings.Repeat("?", 43), strings.Repeat("a", 129), strings.Repeat("a", 42)} {
		if _, e := s.Exchange(context.Background(), code, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		if len(m.sessions) != 0 {
			t.Fatal("issued")
		}
	}
	if _, e := s.Exchange(context.Background(), code, v); e != nil {
		t.Fatal("wrong verifier consumed", e)
	}
}
func TestExpiryAndRevocation(t *testing.T) {
	for _, scenario := range []string{"handoff", "transaction", "bearer", "revoked", "missing-credential", "unknown-code", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, p, clock := setup(t)
			v := strings.Repeat("a", 43)
			code := handoff(t, s, v)
			ctx := context.Background()
			switch scenario {
			case "handoff":
				*clock = clock.Add(HandoffTTL)
			case "transaction":
				for k, tx := range m.transactions {
					tx.ExpiresAt = *clock
					m.transactions[k] = tx
				}
			case "missing-credential":
				p.valid = false
			case "unknown-code":
				code, _ = auth.RandomID()
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			r, e := s.Exchange(ctx, code, v)
			if scenario == "bearer" || scenario == "revoked" {
				if e != nil {
					t.Fatal(e)
				}
				if scenario == "bearer" {
					*clock = clock.Add(SessionTTL)
				} else {
					s.Revoke(r.SessionToken)
				}
				if _, e = s.Resolve(ctx, r.SessionToken); !errors.Is(e, ErrSession) {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatal("bad exchange succeeded")
			}
		})
	}
}
func TestConcurrentExchangeOnlyOneSuccess(t *testing.T) {
	s, m, _, _ := setup(t)
	v := strings.Repeat("a", 43)
	code := handoff(t, s, v)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Exchange(context.Background(), code, v); e == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || len(m.sessions) != 1 {
		t.Fatal(successes.Load(), len(m.sessions))
	}
}
func TestCapacityCleanupAndTombstone(t *testing.T) {
	s, m, _, clock := setup(t)
	m.capacity = 1
	v := strings.Repeat("a", 43)
	first, e := s.Start(context.Background(), Challenge(v), "S256")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Start(context.Background(), Challenge(v), "S256"); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
	u, _ := url.Parse(first.AuthorizationURL)
	tx, _ := m.Transaction(u.Query().Get("transaction"))
	*clock = clock.Add(TransactionTTL)
	if !s.IsMobile(tx.OAuthState) {
		t.Fatal("lost expired mobile marker")
	}
	if _, e = s.Begin(context.Background(), tx.ID, ""); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	*clock = clock.Add(auth.StateLifetime)
	if _, e = s.Start(context.Background(), Challenge(v), "S256"); e != nil {
		t.Fatal("not cleaned", e)
	}
}
func TestSessionCapacityDoesNotConsumeHandoff(t *testing.T) {
	s, m, _, clock := setup(t)
	v := strings.Repeat("a", 43)
	c1 := handoff(t, s, v)
	c2 := handoff(t, s, v)
	m.capacity = 1
	r, e := s.Exchange(context.Background(), c1, v)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Exchange(context.Background(), c2, v); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
	h, _ := m.Handoff(sha256.Sum256([]byte(c2)))
	if h.Consumed {
		t.Fatal("capacity consumed handoff")
	}
	session, _ := m.Session(sha256.Sum256([]byte(r.SessionToken)))
	session.ExpiresAt = *clock
	m.sessions[session.TokenHash] = session
	if _, e = s.Exchange(context.Background(), c2, v); e != nil {
		t.Fatal(e)
	}
}
func TestHandoffCapacityAndReferenceRevoke(t *testing.T) {
	s, m, _, _ := setup(t)
	v := strings.Repeat("a", 43)
	code := handoff(t, s, v)
	r, e := s.Exchange(context.Background(), code, v)
	if e != nil {
		t.Fatal(e)
	}
	s.RevokeReference("credential-ref")
	if _, e = s.Resolve(context.Background(), r.SessionToken); !errors.Is(e, ErrSession) {
		t.Fatal(e)
	}
	m.capacity = 1
	res, e := s.Start(context.Background(), Challenge(v), "S256")
	if !errors.Is(e, ErrCapacity) || res != nil {
		t.Fatal(e)
	}
	m.capacity = 16
	res, e = s.Start(context.Background(), Challenge(v), "S256")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(res.AuthorizationURL)
	tx, e := s.Begin(context.Background(), u.Query().Get("transaction"), "")
	if e != nil {
		t.Fatal(e)
	}
	m.capacity = 1
	if _, e = s.Complete(context.Background(), tx.OAuthState, tx.OAuthState, "fake"); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
}
func TestConfigurationAndCallbackFailures(t *testing.T) {
	for _, change := range []func(*Settings){func(s *Settings) { s.BaseURL = "http://sync.example" }, func(s *Settings) { s.PackageName = "" }, func(s *Settings) { s.SigningSHA256 = "fake" }, func(s *Settings) { s.BaseURL = "https://evil.example" }, func(s *Settings) { s.BaseURL = "https://sync.example/path" }, func(s *Settings) { s.BaseURL = "https://user@sync.example" }} {
		cfg := settings()
		change(&cfg)
		if cfg.Valid() {
			t.Fatal(cfg)
		}
	}
	s, _, p, clock := setup(t)
	v := strings.Repeat("a", 43)
	res, _ := s.Start(context.Background(), Challenge(v), "S256")
	u, _ := url.Parse(res.AuthorizationURL)
	tx, _ := s.Begin(context.Background(), u.Query().Get("transaction"), "")
	if _, e := s.Begin(context.Background(), tx.ID, ""); !errors.Is(e, ErrInvalid) {
		t.Fatal("bootstrap replay", e)
	}
	*clock = clock.Add(TransactionTTL)
	if _, e := s.Complete(context.Background(), tx.OAuthState, tx.OAuthState, "fake"); !errors.Is(e, ErrInvalid) || p.calls.Load() != 0 {
		t.Fatal("expired transaction called provider", e)
	}
}
func TestProviderFailureAndCancel(t *testing.T) {
	for _, failure := range []error{auth.ErrTokenExchange, auth.ErrCodeMissing, auth.ErrStateInvalid} {
		s, _, p, _ := setup(t)
		p.completeErr = failure
		res, _ := s.Start(context.Background(), Challenge(strings.Repeat("a", 43)), "S256")
		u, _ := url.Parse(res.AuthorizationURL)
		tx, _ := s.Begin(context.Background(), u.Query().Get("transaction"), "")
		if _, e := s.Complete(context.Background(), tx.OAuthState, tx.OAuthState, "fake"); !errors.Is(e, failure) {
			t.Fatal(e)
		}
		if _, e := s.Complete(context.Background(), tx.OAuthState, tx.OAuthState, "fake"); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	s, _, _, _ := setup(t)
	res, _ := s.Start(context.Background(), Challenge(strings.Repeat("a", 43)), "S256")
	u, _ := url.Parse(res.AuthorizationURL)
	tx, _ := s.Begin(context.Background(), u.Query().Get("transaction"), "")
	s.Cancel(tx.OAuthState, tx.OAuthState)
	if _, e := s.Complete(context.Background(), tx.OAuthState, tx.OAuthState, "fake"); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}

func TestTransactionBindingAndConcurrentCallback(t *testing.T) {
	for _, field := range []string{"challenge", "method", "flow"} {
		s, m, _, _ := setup(t)
		v := strings.Repeat("a", 43)
		code := handoff(t, s, v)
		for id, tx := range m.transactions {
			switch field {
			case "challenge":
				tx.Challenge = Challenge(strings.Repeat("b", 43))
			case "method":
				tx.ChallengeMethod = "plain"
			case "flow":
				tx.FlowType = "web"
			}
			m.transactions[id] = tx
		}
		if _, err := s.Exchange(context.Background(), code, v); !errors.Is(err, ErrInvalid) {
			t.Fatal(field, err)
		}
	}
	s, _, p, _ := setup(t)
	v := strings.Repeat("a", 43)
	res, _ := s.Start(context.Background(), Challenge(v), "S256")
	u, _ := url.Parse(res.AuthorizationURL)
	tx, _ := s.Begin(context.Background(), u.Query().Get("transaction"), "")
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Complete(context.Background(), tx.OAuthState, tx.OAuthState, "fake"); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || p.calls.Load() != 1 {
		t.Fatal("double provider completion", successes.Load(), p.calls.Load())
	}
}
