package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func TestIngestAcceptsEventsButPublicListenerDoesNot(t *testing.T) {
	broker := newTestBroker(t)
	body := `{"kind":"agent.completed","agent":"builder-1","title":"done"}`

	ingestRequest := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
	ingestResponse := httptest.NewRecorder()
	newIngestMux(broker).ServeHTTP(ingestResponse, ingestRequest)
	if ingestResponse.Code != http.StatusCreated {
		t.Fatalf("ingest returned %d: %s", ingestResponse.Code, ingestResponse.Body.String())
	}

	publicRequest := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
	publicResponse := httptest.NewRecorder()
	newPublicMux(broker).ServeHTTP(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("public listener returned %d, want %d", publicResponse.Code, http.StatusMethodNotAllowed)
	}
}

func TestPublicEventStreamRequiresHTTP3(t *testing.T) {
	broker := newTestBroker(t)
	token := pairTestDevice(t, broker, "phone-1")
	request := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	newPublicMux(broker).ServeHTTP(response, request)

	if response.Code != http.StatusHTTPVersionNotSupported {
		t.Fatalf("stream returned %d, want %d", response.Code, http.StatusHTTPVersionNotSupported)
	}
}

func TestAcknowledgementIsPersisted(t *testing.T) {
	broker := newTestBroker(t)
	token := pairTestDevice(t, broker, "phone-1")
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/ack",
		strings.NewReader(`{"device_id":"phone-1","through":42}`),
	)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	newPublicMux(broker).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("ack returned %d: %s", response.Code, response.Body.String())
	}
}

func TestPublicEndpointsRequirePairedDevice(t *testing.T) {
	broker := newTestBroker(t)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/events", nil),
		httptest.NewRequest(http.MethodPost, "/v1/ack", strings.NewReader(`{"device_id":"phone-1","through":1}`)),
	} {
		response := httptest.NewRecorder()
		newPublicMux(broker).ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s returned %d, want 401", request.URL.Path, response.Code)
		}
	}
}

func TestPairingCodeCanBeRedeemedOnlyOnce(t *testing.T) {
	broker := newTestBroker(t)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	secretHash := sha256.Sum256(secret)
	if err := broker.CreatePairingCode(t.Context(), secretHash[:], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"secret":%q,"device_id":"phone-1","name":"Samsung A54"}`,
		base64.RawURLEncoding.EncodeToString(secret))

	response := httptest.NewRecorder()
	newPublicMux(broker).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/pair", strings.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("pair returned %d: %s", response.Code, response.Body.String())
	}
	var paired struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&paired); err != nil || paired.Token == "" {
		t.Fatalf("invalid pair response %q: %v", response.Body.String(), err)
	}

	replay := httptest.NewRecorder()
	newPublicMux(broker).ServeHTTP(replay, httptest.NewRequest(http.MethodPost, "/v1/pair", strings.NewReader(body)))
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed pair returned %d, want 401", replay.Code)
	}
}

func TestHTTP3StreamsPersistedEvent(t *testing.T) {
	broker := newTestBroker(t)
	token := pairTestDevice(t, broker, "phone-1")
	want, err := broker.Publish(t.Context(), events.Input{
		Kind:  "agent.completed",
		Agent: "builder-1",
		Title: "done",
	})
	if err != nil {
		t.Fatal(err)
	}

	packetConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http3.Server{
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{testCertificate(t)},
		},
		Handler:    newPublicMux(broker),
		QUICConfig: newPublicQUICConfig(),
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(packetConn) }()
	t.Cleanup(func() {
		server.Close()
		packetConn.Close()
		select {
		case <-serveErrors:
		case <-time.After(time.Second):
			t.Error("HTTP/3 server did not stop")
		}
	})

	transport := &http3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Test-only certificate.
	}
	t.Cleanup(func() { transport.Close() })
	client := http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodGet, "https://"+packetConn.LocalAddr().String()+"/v1/events?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 3 {
		t.Fatalf("negotiated HTTP/%d, want HTTP/3", response.ProtoMajor)
	}
	var got events.Event
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Title != want.Title {
		t.Fatalf("streamed event %#v, want %#v", got, want)
	}
}

func TestHardenedHTTP3AllowsAuthenticatedReconnect(t *testing.T) {
	broker := newTestBroker(t)
	token := pairTestDevice(t, broker, "phone-1")
	address := startHardenedHTTP3Server(t, newPublicMux(broker))
	udpAddress, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	garbageConn, err := net.DialUDP("udp4", nil, udpAddress)
	if err != nil {
		t.Fatal(err)
	}
	garbage := make([]byte, 1200)
	for i := range garbage {
		garbage[i] = byte(i)
	}
	for range 64 {
		if _, err := garbageConn.Write(garbage); err != nil {
			garbageConn.Close()
			t.Fatal(err)
		}
	}
	garbageConn.Close()

	for connection := 1; connection <= 2; connection++ {
		transport := &http3.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Test-only certificate.
			QUICConfig:      newPublicQUICConfig(),
		}
		client := http.Client{Transport: transport, Timeout: 5 * time.Second}
		request, err := http.NewRequest(
			http.MethodPost,
			"https://"+address+"/v1/ack",
			strings.NewReader(`{"device_id":"phone-1","through":1}`),
		)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		if err != nil {
			transport.Close()
			t.Fatalf("connection %d failed: %v", connection, err)
		}
		response.Body.Close()
		transport.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("connection %d returned %d", connection, response.StatusCode)
		}
	}
}

func startHardenedHTTP3Server(t *testing.T, handler http.Handler) string {
	t.Helper()
	packetConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	security := newPublicSecurity()
	transport := &quic.Transport{
		Conn:                packetConn,
		VerifySourceAddress: security.verifySourceAddress,
		ConnContext:         security.connectionContext,
		MaxTokenAge:         12 * time.Hour,
	}
	listener, err := transport.ListenEarly(
		http3.ConfigureTLSConfig(&tls.Config{
			Certificates: []tls.Certificate{testCertificate(t)},
			MinVersion:   tls.VersionTLS13,
		}),
		newPublicQUICConfig(),
	)
	if err != nil {
		packetConn.Close()
		t.Fatal(err)
	}
	server := &http3.Server{
		Handler:        handler,
		QUICConfig:     newPublicQUICConfig(),
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 16 << 10,
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.ServeListener(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		server.Shutdown(ctx)
		listener.Close()
		transport.Close()
		packetConn.Close()
		select {
		case <-serveErrors:
		case <-time.After(time.Second):
			t.Error("hardened HTTP/3 server did not stop")
		}
	})
	return packetConn.LocalAddr().String()
}

func pairTestDevice(t *testing.T, broker *events.Broker, deviceID string) string {
	t.Helper()
	secretHash := sha256.Sum256([]byte("test pairing secret " + deviceID))
	token := []byte("01234567890123456789012345678901")
	tokenHash := sha256.Sum256(token)
	if err := broker.CreatePairingCode(t.Context(), secretHash[:], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := broker.RedeemPairingCode(t.Context(), secretHash[:], deviceID, "test device", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(token)
}

func newTestBroker(t *testing.T) *events.Broker {
	t.Helper()
	journal, err := events.OpenSQLite(t.TempDir() + "/events.db")
	if err != nil {
		t.Fatal(err)
	}
	broker := events.NewBroker(journal)
	t.Cleanup(func() { broker.Close() })
	return broker
}

func testCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "migi test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificate, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{
		Certificate: [][]byte{certificate},
		PrivateKey:  privateKey,
	}
}
