package admin

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

func TestDashboardAndPairing(t *testing.T) {
	handler, broker := newTestHandler(t)

	request := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "Migi") ||
		!strings.Contains(response.Body.String(), "https://203.0.113.10:443") ||
		!strings.Contains(response.Body.String(), "Send test notification") {
		t.Fatalf("dashboard is missing expected server details: %s", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("dashboard has no Content-Security-Policy")
	}

	form := url.Values{"csrf_token": {handler.csrfToken}, "ttl": {"10m"}}
	request = httptest.NewRequest(http.MethodPost, "/admin/pair", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("pairing status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatal("pairing page has no embedded QR image")
	}
	if strings.Contains(body, "migi://pair") || strings.Contains(body, "secret=") {
		t.Fatal("pairing secret leaked into the HTML response")
	}
	stats, err := broker.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.ActivePairingCodes != 1 {
		t.Fatalf("active pairing codes = %d, want 1", stats.ActivePairingCodes)
	}
}

func TestAdminRejectsInvalidCSRF(t *testing.T) {
	handler, _ := newTestHandler(t)
	form := url.Values{"csrf_token": {"wrong"}, "ttl": {"10m"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/pair", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestAdminSendsTestNotification(t *testing.T) {
	handler, broker := newTestHandler(t)
	form := url.Values{"csrf_token": {handler.csrfToken}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/admin/notifications/test",
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusSeeOther, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/admin/?notice=Test+notification+sent" {
		t.Fatalf("Location = %q", location)
	}
	stats, err := broker.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 1 || stats.LatestEventID != 1 {
		t.Fatalf("unexpected stats after test notification: %#v", stats)
	}
}

func TestAdminRevokesDevice(t *testing.T) {
	handler, broker := newTestHandler(t)
	secretHash := sha256.Sum256([]byte("pairing secret"))
	tokenHash := sha256.Sum256([]byte("device token"))
	if err := broker.CreatePairingCode(context.Background(), secretHash[:], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := broker.RedeemPairingCode(
		context.Background(), secretHash[:], "phone-1", "Samsung A54", tokenHash[:],
	); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"csrf_token": {handler.csrfToken}, "device_id": {"phone-1"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/devices/revoke", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusSeeOther, response.Body.String())
	}
	if _, err := broker.AuthenticateDevice(context.Background(), tokenHash[:]); !errors.Is(err, events.ErrUnauthorized) {
		t.Fatalf("authenticate revoked device error = %v, want %v", err, events.ErrUnauthorized)
	}
}

func TestPublicEndpointValidation(t *testing.T) {
	valid, err := parsePublicEndpoint("https://192.0.2.1:10443/")
	if err != nil || valid.String() != "https://192.0.2.1:10443" {
		t.Fatalf("valid endpoint = %v, %v", valid, err)
	}
	for _, value := range []string{"http://192.0.2.1", "https://user@host", "https://host/path"} {
		if _, err := parsePublicEndpoint(value); err == nil {
			t.Errorf("parsePublicEndpoint(%q) succeeded", value)
		}
	}
}

func newTestHandler(t *testing.T) (*Handler, *events.Broker) {
	t.Helper()
	journal, err := events.OpenSQLite(filepath.Join(t.TempDir(), "migi.db"))
	if err != nil {
		t.Fatal(err)
	}
	broker := events.NewBroker(journal)
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Error(err)
		}
	})
	handler, err := New(Config{
		Broker:                 broker,
		PublicEndpoint:         "https://203.0.113.10:443",
		CertificateFingerprint: "AA:BB:CC:DD",
		PublicListen:           ":8443",
		IngestListen:           "127.0.0.1:8787",
		AdminListen:            "127.0.0.1:8788",
		StartedAt:              time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, broker
}
