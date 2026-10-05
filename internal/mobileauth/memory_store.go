package mobileauth

import (
	"context"
	"crypto/subtle"
	"sync"
	"time"

	"example.com/sync/internal/auth"
)

type MemoryStore struct {
	mu           sync.Mutex
	transactions map[string]Transaction
	handoffs     map[[32]byte]Handoff
	sessions     map[[32]byte]Session
	capacity     int
	now          func() time.Time
}

func NewMemoryStore(capacity int) *MemoryStore {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &MemoryStore{transactions: map[string]Transaction{}, handoffs: map[[32]byte]Handoff{}, sessions: map[[32]byte]Session{}, capacity: capacity, now: time.Now}
}
func (m *MemoryStore) cleanup() {
	now := m.now()
	// Retain expired mobile state tombstones until the shared provider's longer
	// state TTL has passed: expired mobile callbacks must never become web flows.
	for k, t := range m.transactions {
		if !now.Before(t.ExpiresAt.Add(auth.StateLifetime)) {
			delete(m.transactions, k)
		}
	}
	for k, h := range m.handoffs {
		if !now.Before(h.ExpiresAt) {
			delete(m.handoffs, k)
		}
	}
	for k, s := range m.sessions {
		if !now.Before(s.ExpiresAt) {
			delete(m.sessions, k)
		}
	}
}
func (m *MemoryStore) Create(t Transaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	if len(m.transactions) >= m.capacity {
		return ErrCapacity
	}
	m.transactions[t.ID] = t
	return nil
}
func (m *MemoryStore) Transaction(id string) (Transaction, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	t, ok := m.transactions[id]
	return t, ok
}
func (m *MemoryStore) Handoff(hash [32]byte) (Handoff, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	h, ok := m.handoffs[hash]
	return h, ok
}
func (m *MemoryStore) Session(hash [32]byte) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	s, ok := m.sessions[hash]
	active := ok && s.RevokedAt.IsZero() && m.now().Before(s.ExpiresAt)
	if active {
		s.LastUsedAt = m.now()
		m.sessions[hash] = s
	}
	return s, active
}
func (m *MemoryStore) Begin(id string) (Transaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	t, ok := m.transactions[id]
	if !ok || t.Phase != "created" || !m.now().Before(t.ExpiresAt) {
		return Transaction{}, ErrInvalid
	}
	t.Phase = "browser_started"
	m.transactions[id] = t
	return t, nil
}
func (m *MemoryStore) Recognizes(state string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	for _, t := range m.transactions {
		if t.OAuthState == state {
			return true
		}
	}
	return false
}
func (m *MemoryStore) ClaimCallback(state string) (Transaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	for id, t := range m.transactions {
		if t.OAuthState == state {
			if t.Phase != "browser_started" || !m.now().Before(t.ExpiresAt) {
				return Transaction{}, ErrInvalid
			}
			t.Phase = "processing"
			m.transactions[id] = t
			return t, nil
		}
	}
	return Transaction{}, ErrInvalid
}
func (m *MemoryStore) Fail(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.transactions[id]; ok {
		t.Phase = "failed"
		m.transactions[id] = t
	}
}
func (m *MemoryStore) Complete(id string, h Handoff) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	t, ok := m.transactions[id]
	if !ok || t.Phase != "processing" || !m.now().Before(t.ExpiresAt) || h.TransactionID != id || h.Challenge != t.Challenge || h.CredentialReference == "" {
		return ErrInvalid
	}
	if len(m.handoffs) >= m.capacity {
		return ErrCapacity
	}
	t.Phase = "completed"
	m.transactions[id] = t
	m.handoffs[h.CodeHash] = h
	return nil
}
func (m *MemoryStore) Exchange(ctx context.Context, hash [32]byte, verifier string, s Session, exists func(string) (bool, error)) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	h, ok := m.handoffs[hash]
	t, tok := m.transactions[h.TransactionID]
	if !ok || h.Consumed || !m.now().Before(h.ExpiresAt) || !tok || t.Phase != "completed" || t.FlowType != "mobile" || t.ChallengeMethod != "S256" || !m.now().Before(t.ExpiresAt) || h.Challenge != t.Challenge {
		return Session{}, ErrInvalid
	}
	computed := Challenge(verifier)
	if !ValidVerifier(verifier) || subtle.ConstantTimeCompare([]byte(computed), []byte(h.Challenge)) != 1 {
		return Session{}, ErrInvalid
	}
	valid, err := exists(h.CredentialReference)
	if err != nil {
		return Session{}, err
	}
	if !valid {
		return Session{}, ErrInvalid
	}
	if len(m.sessions) >= m.capacity {
		return Session{}, ErrCapacity
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	s.CredentialReference = h.CredentialReference
	h.Consumed = true
	t.Phase = "consumed"
	m.handoffs[hash] = h
	m.transactions[t.ID] = t
	m.sessions[s.TokenHash] = s
	return s, nil
}
func (m *MemoryStore) Revoke(hash [32]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	if s, ok := m.sessions[hash]; ok {
		s.RevokedAt = m.now()
		m.sessions[hash] = s
	}
}
func (m *MemoryStore) RevokeReference(ref string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	for k, s := range m.sessions {
		if s.CredentialReference == ref {
			s.RevokedAt = m.now()
			m.sessions[k] = s
		}
	}
}
