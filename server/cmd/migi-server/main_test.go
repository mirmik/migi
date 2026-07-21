package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
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
	request := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	response := httptest.NewRecorder()
	newPublicMux(newTestBroker(t)).ServeHTTP(response, request)

	if response.Code != http.StatusHTTPVersionNotSupported {
		t.Fatalf("stream returned %d, want %d", response.Code, http.StatusHTTPVersionNotSupported)
	}
}

func TestAcknowledgementIsPersisted(t *testing.T) {
	broker := newTestBroker(t)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/ack",
		strings.NewReader(`{"device_id":"phone-1","through":42}`),
	)
	response := httptest.NewRecorder()
	newPublicMux(broker).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("ack returned %d: %s", response.Code, response.Body.String())
	}
}

func TestHTTP3StreamsPersistedEvent(t *testing.T) {
	broker := newTestBroker(t)
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
		Handler: newPublicMux(broker),
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
	response, err := client.Get("https://" + packetConn.LocalAddr().String() + "/v1/events?after=0")
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
