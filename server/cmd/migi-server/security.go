package main

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	publicMaxConnections            = 64
	publicMaxUnvalidatedConnections = 48
	publicMaxConnectionsPerSource   = 8
	publicMaxConcurrentRequests     = 128
	publicRateLimitEntries          = 4096
)

var errConnectionCapacity = errors.New("public QUIC connection capacity exceeded")

type publicSecurity struct {
	connections  *connectionAdmission
	handshakes   *keyedRateLimiter
	pairRequests *keyedRateLimiter
	healthChecks *keyedRateLimiter
	authAttempts *keyedRateLimiter
	authFailures *keyedRateLimiter
	requestSlots chan struct{}
}

func newPublicSecurity() *publicSecurity {
	return &publicSecurity{
		connections: newConnectionAdmission(
			publicMaxConnections,
			publicMaxUnvalidatedConnections,
			publicMaxConnectionsPerSource,
		),
		// A burst above these handshake rates is required to prove its source
		// address with QUIC Retry before it can consume a reserved connection slot.
		handshakes:   newKeyedRateLimiter(50, 100, 5, 10),
		pairRequests: newKeyedRateLimiter(20, 40, 2, 4),
		healthChecks: newKeyedRateLimiter(50, 100, 5, 10),
		authAttempts: newKeyedRateLimiter(100, 200, 10, 20),
		authFailures: newKeyedRateLimiter(20, 40, 2, 5),
		requestSlots: make(chan struct{}, publicMaxConcurrentRequests),
	}
}

func (s *publicSecurity) verifySourceAddress(addr net.Addr) bool {
	// VerifySourceAddress returns true when Retry is required. Normal phone
	// reconnects avoid the extra round trip; bursts are forced to validate.
	return !s.handshakes.allowAddr(addr)
}

func (s *publicSecurity) connectionContext(
	ctx context.Context,
	info *quic.ClientInfo,
) (context.Context, error) {
	if info == nil || !s.connections.admit(ctx, sourceKeyForAddr(info.RemoteAddr), info.AddrVerified) {
		return nil, errConnectionCapacity
	}
	return ctx, nil
}

func (s *publicSecurity) rateLimit(
	scope string,
	limiter *keyedRateLimiter,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allowRemote(r.RemoteAddr) {
			slog.Warn("public request rate limited", "scope", scope, "remote_addr", r.RemoteAddr)
			writeRateLimited(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *publicSecurity) limitConcurrency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.requestSlots <- struct{}{}:
			defer func() { <-s.requestSlots }()
			next.ServeHTTP(w, r)
		default:
			slog.Warn("public request capacity reached", "remote_addr", r.RemoteAddr)
			writeRateLimited(w)
		}
	})
}

func writeRateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "request rate limit exceeded", http.StatusTooManyRequests)
}

type connectionAdmission struct {
	mu             sync.Mutex
	maxTotal       int
	maxUnvalidated int
	maxPerSource   int
	active         int
	unvalidated    int
	bySource       map[string]int
}

func newConnectionAdmission(maxTotal, maxUnvalidated, maxPerSource int) *connectionAdmission {
	return &connectionAdmission{
		maxTotal:       maxTotal,
		maxUnvalidated: maxUnvalidated,
		maxPerSource:   maxPerSource,
		bySource:       make(map[string]int),
	}
}

func (a *connectionAdmission) admit(ctx context.Context, source string, verified bool) bool {
	a.mu.Lock()
	if a.active >= a.maxTotal ||
		(!verified && a.unvalidated >= a.maxUnvalidated) ||
		a.bySource[source] >= a.maxPerSource {
		a.mu.Unlock()
		return false
	}
	a.active++
	if !verified {
		a.unvalidated++
	}
	a.bySource[source]++
	a.mu.Unlock()

	context.AfterFunc(ctx, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.active--
		if !verified {
			a.unvalidated--
		}
		a.bySource[source]--
		if a.bySource[source] == 0 {
			delete(a.bySource, source)
		}
	})
	return true
}

type keyedRateLimiter struct {
	global  *tokenBuckets
	sources *tokenBuckets
}

func newKeyedRateLimiter(globalRate float64, globalBurst int, sourceRate float64, sourceBurst int) *keyedRateLimiter {
	return &keyedRateLimiter{
		global:  newTokenBuckets(globalRate, globalBurst, 1),
		sources: newTokenBuckets(sourceRate, sourceBurst, publicRateLimitEntries),
	}
}

func (l *keyedRateLimiter) allowRemote(remoteAddr string) bool {
	return l.allow(sourceKey(remoteAddr))
}

func (l *keyedRateLimiter) allowAddr(addr net.Addr) bool {
	return l.allow(sourceKeyForAddr(addr))
}

func (l *keyedRateLimiter) allow(source string) bool {
	if !l.global.allow("global") {
		return false
	}
	return l.sources.allow(source)
}

func (l *keyedRateLimiter) readyRemote(remoteAddr string) bool {
	return l.global.ready("global") && l.sources.ready(sourceKey(remoteAddr))
}

type tokenBucket struct {
	tokens  float64
	updated time.Time
	element *list.Element
}

type tokenBuckets struct {
	mu         sync.Mutex
	rate       float64
	burst      float64
	maxEntries int
	now        func() time.Time
	entries    map[string]*tokenBucket
	order      *list.List
}

func newTokenBuckets(rate float64, burst, maxEntries int) *tokenBuckets {
	return &tokenBuckets{
		rate:       rate,
		burst:      float64(burst),
		maxEntries: maxEntries,
		now:        time.Now,
		entries:    make(map[string]*tokenBucket),
		order:      list.New(),
	}
}

func (b *tokenBuckets) allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	bucket := b.bucket(key, b.now())
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func (b *tokenBuckets) ready(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bucket(key, b.now()).tokens >= 1
}

func (b *tokenBuckets) bucket(key string, now time.Time) *tokenBucket {
	if existing := b.entries[key]; existing != nil {
		elapsed := now.Sub(existing.updated).Seconds()
		if elapsed > 0 {
			existing.tokens = math.Min(b.burst, existing.tokens+elapsed*b.rate)
			existing.updated = now
		}
		b.order.MoveToBack(existing.element)
		return existing
	}
	if len(b.entries) >= b.maxEntries {
		b.evictOldest()
	}
	created := &tokenBucket{tokens: b.burst, updated: now}
	created.element = b.order.PushBack(key)
	b.entries[key] = created
	return created
}

func (b *tokenBuckets) evictOldest() {
	oldest := b.order.Front()
	if oldest != nil {
		if key, ok := oldest.Value.(string); ok {
			delete(b.entries, key)
			b.order.Remove(oldest)
		}
	}
}

func sourceKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return "unknown"
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	return addr.Unmap().String()
}

func sourceKeyForAddr(addr net.Addr) string {
	if addr == nil {
		return "unknown"
	}
	return sourceKey(addr.String())
}
