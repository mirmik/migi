package main

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicQUICConfigHasExplicitBounds(t *testing.T) {
	config := newPublicQUICConfig()
	if config.HandshakeIdleTimeout != 5*time.Second {
		t.Fatalf("handshake timeout = %s", config.HandshakeIdleTimeout)
	}
	if config.MaxIdleTimeout != 2*time.Minute || config.KeepAlivePeriod != 30*time.Second {
		t.Fatalf("idle settings = %s / %s", config.MaxIdleTimeout, config.KeepAlivePeriod)
	}
	if config.MaxIncomingStreams != 16 || config.MaxIncomingUniStreams != 8 {
		t.Fatalf("stream limits = %d / %d", config.MaxIncomingStreams, config.MaxIncomingUniStreams)
	}
	if config.MaxStreamReceiveWindow != 256<<10 || config.MaxConnectionReceiveWindow != 1<<20 {
		t.Fatalf("receive windows = %d / %d", config.MaxStreamReceiveWindow, config.MaxConnectionReceiveWindow)
	}
	if config.Allow0RTT {
		t.Fatal("0-RTT must remain disabled for state-changing public endpoints")
	}
}

func TestConnectionAdmissionReservesCapacityForValidatedReconnects(t *testing.T) {
	admission := newConnectionAdmission(3, 2, 1)
	first, cancelFirst := context.WithCancel(t.Context())
	second, cancelSecond := context.WithCancel(t.Context())
	validated, cancelValidated := context.WithCancel(t.Context())
	defer cancelSecond()
	defer cancelValidated()

	if !admission.admit(first, "192.0.2.1", false) {
		t.Fatal("first connection was rejected")
	}
	if admission.admit(t.Context(), "192.0.2.1", true) {
		t.Fatal("per-source connection limit was not enforced")
	}
	if !admission.admit(second, "192.0.2.2", false) {
		t.Fatal("second unvalidated connection was rejected")
	}
	if admission.admit(t.Context(), "192.0.2.3", false) {
		t.Fatal("unvalidated connection consumed the validated reserve")
	}
	if !admission.admit(validated, "192.0.2.3", true) {
		t.Fatal("validated reconnect could not use its reserved slot")
	}

	cancelFirst()
	deadline := time.Now().Add(time.Second)
	for {
		probe, cancelProbe := context.WithCancel(t.Context())
		if admission.admit(probe, "192.0.2.4", false) {
			cancelProbe()
			break
		}
		cancelProbe()
		if time.Now().After(deadline) {
			t.Fatal("closed connection did not release admission capacity")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSourceAddressValidationStartsUnderBurst(t *testing.T) {
	security := newPublicSecurity()
	security.handshakes = fixedLimiter(2)
	remote := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 44321}

	if security.verifySourceAddress(remote) || security.verifySourceAddress(remote) {
		t.Fatal("normal reconnect burst unexpectedly required Retry")
	}
	if !security.verifySourceAddress(remote) {
		t.Fatal("connection burst did not require QUIC source-address validation")
	}
}

func TestPublicHealthBurstIsBounded(t *testing.T) {
	broker := newTestBroker(t)
	security := newPublicSecurity()
	security.healthChecks = fixedLimiter(4)
	handler := newPublicMuxWithSecurity(broker, security)

	const requests = 40
	var successful atomic.Int64
	var limited atomic.Int64
	var wait sync.WaitGroup
	for range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			request.RemoteAddr = "192.0.2.20:50000"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			switch response.Code {
			case http.StatusOK:
				successful.Add(1)
			case http.StatusTooManyRequests:
				limited.Add(1)
			default:
				t.Errorf("health response = %d", response.Code)
			}
		}()
	}
	wait.Wait()
	if successful.Load() != 4 || limited.Load() != requests-4 {
		t.Fatalf("successful / limited = %d / %d", successful.Load(), limited.Load())
	}
}

func TestMalformedPairingBurstIsRejectedBeforeMoreParsing(t *testing.T) {
	broker := newTestBroker(t)
	security := newPublicSecurity()
	security.pairRequests = fixedLimiter(2)
	handler := newPublicMuxWithSecurity(broker, security)

	for attempt, want := range []int{
		http.StatusBadRequest,
		http.StatusBadRequest,
		http.StatusTooManyRequests,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/pair", http.NoBody)
		request.RemoteAddr = "192.0.2.25:50000"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("attempt %d returned %d, want %d", attempt+1, response.Code, want)
		}
	}
}

func TestFailedAuthenticationBurstStopsBeforeJournal(t *testing.T) {
	broker := newTestBroker(t)
	security := newPublicSecurity()
	security.authFailures = fixedLimiter(2)
	security.authAttempts = fixedLimiter(20)
	handler := newPublicMuxWithSecurity(broker, security)
	credential := base64.RawURLEncoding.EncodeToString([]byte("0123456789012345678901234567890x"))

	for attempt, want := range []int{
		http.StatusUnauthorized,
		http.StatusUnauthorized,
		http.StatusTooManyRequests,
		http.StatusTooManyRequests,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/ack", nil)
		request.RemoteAddr = "192.0.2.30:50000"
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("attempt %d returned %d, want %d", attempt+1, response.Code, want)
		}
		if want == http.StatusTooManyRequests && response.Header().Get("Retry-After") != "1" {
			t.Fatal("rate-limited authentication response has no Retry-After")
		}
	}
}

func TestPublicConcurrencyGateRejectsExcessWork(t *testing.T) {
	security := newPublicSecurity()
	security.requestSlots = make(chan struct{}, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	handler := security.limitConcurrency(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-started
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("excess request returned %d", second.Code)
	}
	close(release)
	<-firstDone
}

func TestTokenBucketEntryCountIsBounded(t *testing.T) {
	buckets := newTokenBuckets(1, 1, 2)
	fixed := time.Unix(1, 0)
	buckets.now = func() time.Time { return fixed }
	buckets.allow("one")
	buckets.allow("two")
	buckets.ready("one")
	buckets.allow("three")
	if len(buckets.entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(buckets.entries))
	}
	if buckets.entries["one"] == nil || buckets.entries["three"] == nil || buckets.entries["two"] != nil {
		t.Fatal("bounded limiter did not evict the least recently used source")
	}
}

func TestSourceKeyNormalizesMappedIPv4(t *testing.T) {
	if got := sourceKey("[::ffff:192.0.2.1]:443"); got != "192.0.2.1" {
		t.Fatalf("source key = %q", got)
	}
}

func fixedLimiter(burst int) *keyedRateLimiter {
	limiter := newKeyedRateLimiter(1, burst, 1, burst)
	fixed := time.Unix(1, 0)
	limiter.global.now = func() time.Time { return fixed }
	limiter.sources.now = func() time.Time { return fixed }
	return limiter
}
