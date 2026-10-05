package lastfmeval

import (
	"errors"
	"net/http"
	"sync/atomic"
)

var ErrAuditHTTPBudget = errors.New("audit HTTP attempt budget exhausted")

// Counts RoundTrips, including failed attempts and retries. Do not log request URLs.
type AuditHTTPBudget struct {
	Limit int64
	Used  atomic.Int64
	Base  http.RoundTripper
}

func (b *AuditHTTPBudget) RoundTrip(r *http.Request) (*http.Response, error) {
	for {
		n := b.Used.Load()
		if n >= b.Limit {
			return nil, ErrAuditHTTPBudget
		}
		if b.Used.CompareAndSwap(n, n+1) {
			break
		}
	}
	base := b.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}
