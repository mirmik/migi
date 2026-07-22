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
	connections   *connectionAdmission
	handshakes    *keyedRateLimiter
	pairRequests  *keyedRateLimiter
	healthChecks  *keyedRateLimiter
	authAttempts  *keyedRateLimiter
	authFailures  *keyedRateLimiter
	rejectionLogs *keyedRateLimiter
	deviceStreams *deviceStreamRegistry
	requestSlots  chan struct{}
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
		// Rejected traffic must not turn into an unbounded log-writing workload.
		rejectionLogs: newKeyedRateLimiter(5, 20, 0.2, 2),
		deviceStreams: newDeviceStreamRegistry(),
		requestSlots:  make(chan struct{}, publicMaxConcurrentRequests),
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
			s.logRejection("public request rate limited", r.RemoteAddr, "scope", scope)
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
			s.logRejection("public request capacity reached", r.RemoteAddr)
			writeRateLimited(w)
		}
	})
}

func (s *publicSecurity) limitDeviceStreams(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		device, ok := r.Context().Value(deviceContextKey{}).(authenticatedDevice)
		if !ok {
			http.Error(w, "device authentication required", http.StatusUnauthorized)
			return
		}
		streamContext, streamID, replaced := s.deviceStreams.replace(r.Context(), device.ID)
		if replaced {
			slog.Info("superseding previous device event stream",
				"device_id", device.ID,
				"remote_addr", r.RemoteAddr,
			)
		}
		defer s.deviceStreams.release(device.ID, streamID)
		next.ServeHTTP(w, r.WithContext(streamContext))
	})
}

func (s *publicSecurity) logRejection(message, remoteAddr string, attributes ...any) {
	if !s.rejectionLogs.allowRemote(remoteAddr) {
		return
	}
	attributes = append(attributes, "remote_addr", remoteAddr)
	slog.Warn(message, attributes...)
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

type deviceStream struct {
	id     uint64
	cancel context.CancelFunc
}

type deviceStreamRegistry struct {
	mu       sync.Mutex
	nextID   uint64
	byDevice map[string]deviceStream
}

func newDeviceStreamRegistry() *deviceStreamRegistry {
	return &deviceStreamRegistry{byDevice: make(map[string]deviceStream)}
}

func (a *deviceStreamRegistry) replace(parent context.Context, deviceID string) (context.Context, uint64, bool) {
	ctx, cancel := context.WithCancel(parent)
	a.mu.Lock()
	previous, replaced := a.byDevice[deviceID]
	a.nextID++
	streamID := a.nextID
	a.byDevice[deviceID] = deviceStream{id: streamID, cancel: cancel}
	a.mu.Unlock()
	if replaced {
		previous.cancel()
	}
	return ctx, streamID, replaced

}

func (a *deviceStreamRegistry) release(deviceID string, streamID uint64) {
	a.mu.Lock()
	stream, current := a.byDevice[deviceID]
	if current && stream.id == streamID {
		delete(a.byDevice, deviceID)
	}
	a.mu.Unlock()
	if current && stream.id == streamID {
		stream.cancel()
	}
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
	// Reserve both tokens as one operation. In particular, traffic that has
	// exhausted its source bucket must not consume capacity shared by other
	// sources.
	l.sources.mu.Lock()
	defer l.sources.mu.Unlock()
	sourceBucket := l.sources.bucket(source, l.sources.now())
	if sourceBucket.tokens < 1 {
		return false
	}

	l.global.mu.Lock()
	defer l.global.mu.Unlock()
	globalBucket := l.global.bucket("global", l.global.now())
	if globalBucket.tokens < 1 {
		return false
	}

	sourceBucket.tokens--
	globalBucket.tokens--
	return true
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
