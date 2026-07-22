package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mirmik/migi/server/internal/admin"
	"github.com/mirmik/migi/server/internal/events"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/qlog"
)

func main() {
	if err := run(); err != nil {
		slog.Error("migi server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", ":8443", "UDP address for the HTTP/3 server")
	ingestListen := flag.String("ingest-listen", "127.0.0.1:8787", "trusted local TCP address for event submission")
	adminListen := flag.String("admin-listen", "127.0.0.1:8788", "local TCP address for the administration UI; empty disables it")
	publicEndpoint := flag.String("public-endpoint", "", "public https://host[:port] inserted into pairing invitations")
	databasePath := flag.String("db", "migi.db", "SQLite event journal path")
	cert := flag.String("cert", "", "TLS certificate chain in PEM format")
	key := flag.String("key", "", "TLS private key in PEM format")
	flag.Parse()

	if *cert == "" || *key == "" {
		return errors.New("-cert and -key are required")
	}

	journal, err := events.OpenSQLite(*databasePath)
	if err != nil {
		return err
	}
	broker := events.NewBroker(journal)
	defer broker.Close()
	startedAt := time.Now()
	fingerprint, err := certificateFingerprint(*cert)
	if err != nil {
		return err
	}
	slog.Info("server configured",
		"public_endpoint", *publicEndpoint,
		"database", *databasePath,
		"certificate_fingerprint", fingerprint,
	)
	if *publicEndpoint == "" {
		slog.Warn("public endpoint is not configured; web pairing will be unavailable")
	}

	publicSecurity := newPublicSecurity()
	publicMux := newPublicMuxWithSecurity(broker, publicSecurity)
	quicConfig := newPublicQUICConfig()
	if os.Getenv("QLOGDIR") != "" {
		quicConfig.Tracer = qlog.DefaultConnectionTracer
	}
	tlsCertificate, err := tls.LoadX509KeyPair(*cert, *key)
	if err != nil {
		return fmt.Errorf("load TLS certificate and key: %w", err)
	}
	packetConn, err := net.ListenPacket("udp", *listen)
	if err != nil {
		return fmt.Errorf("listen for public QUIC traffic: %w", err)
	}
	quicTransport := &quic.Transport{
		Conn:                packetConn,
		VerifySourceAddress: publicSecurity.verifySourceAddress,
		ConnContext:         publicSecurity.connectionContext,
		MaxTokenAge:         12 * time.Hour,
	}
	quicListener, err := quicTransport.ListenEarly(
		http3.ConfigureTLSConfig(&tls.Config{
			Certificates: []tls.Certificate{tlsCertificate},
			MinVersion:   tls.VersionTLS13,
		}),
		quicConfig,
	)
	if err != nil {
		packetConn.Close()
		return fmt.Errorf("configure public QUIC listener: %w", err)
	}
	defer packetConn.Close()
	defer quicTransport.Close()
	defer quicListener.Close()
	quicServer := http3.Server{
		Addr:           *listen,
		Handler:        publicMux,
		QUICConfig:     quicConfig,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 16 << 10,
	}
	ingestServer := http.Server{
		Addr:              *ingestListen,
		Handler:           newIngestMux(broker),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	var adminServer *http.Server
	if *adminListen != "" {
		adminHandler, err := admin.New(admin.Config{
			Broker:                 broker,
			PublicEndpoint:         *publicEndpoint,
			CertificateFingerprint: fingerprint,
			PublicListen:           *listen,
			IngestListen:           *ingestListen,
			AdminListen:            *adminListen,
			StartedAt:              startedAt,
		})
		if err != nil {
			return fmt.Errorf("configure admin UI: %w", err)
		}
		adminServer = &http.Server{
			Addr:              *adminListen,
			Handler:           adminHandler.Routes(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       30 * time.Second,
			MaxHeaderBytes:    16 << 10,
		}
	}

	serverErrors := make(chan error, 3)
	go func() {
		slog.Info("starting trusted local ingest", "address", *ingestListen)
		serverErrors <- ingestServer.ListenAndServe()
	}()
	go func() {
		slog.Info("starting public HTTP/3 server", "address", *listen)
		serverErrors <- quicServer.ServeListener(quicListener)
	}()
	if adminServer != nil {
		go func() {
			slog.Info("starting local administration UI", "address", *adminListen)
			serverErrors <- adminServer.ListenAndServe()
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var serveErr error
	select {
	case <-ctx.Done():
		slog.Info("shutting down")
	case serveErr = <-serverErrors:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ingestErr := ingestServer.Shutdown(shutdownContext)
	quicErr := quicServer.Shutdown(shutdownContext)
	var adminErr error
	if adminServer != nil {
		adminErr = adminServer.Shutdown(shutdownContext)
	}
	return errors.Join(serveErr, ingestErr, quicErr, adminErr)
}

func newPublicQUICConfig() *quic.Config {
	return &quic.Config{
		HandshakeIdleTimeout:           5 * time.Second,
		MaxIdleTimeout:                 2 * time.Minute,
		KeepAlivePeriod:                30 * time.Second,
		InitialStreamReceiveWindow:     64 << 10,
		MaxStreamReceiveWindow:         256 << 10,
		InitialConnectionReceiveWindow: 256 << 10,
		MaxConnectionReceiveWindow:     1 << 20,
		MaxIncomingStreams:             16,
		MaxIncomingUniStreams:          8,
		Allow0RTT:                      false,
	}
}

func certificateFingerprint(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read TLS certificate: %w", err)
	}
	block, _ := pem.Decode(contents)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("TLS certificate file does not start with a PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse TLS certificate: %w", err)
	}
	fingerprint := sha256.Sum256(certificate.Raw)
	return admin.NormalizeFingerprint(fingerprint[:]), nil
}

func newPublicMux(broker *events.Broker) http.Handler {
	return newPublicMuxWithSecurity(broker, newPublicSecurity())
}

func newPublicMuxWithSecurity(broker *events.Broker, security *publicSecurity) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", security.rateLimit("health", security.healthChecks, healthHandler(broker)))
	mux.Handle("POST /v1/pair", security.rateLimit("pair", security.pairRequests, pairHandler(broker)))
	mux.Handle("GET /v1/events", authenticateDevice(broker, security, streamHandler(broker)))
	mux.Handle("POST /v1/ack", authenticateDevice(broker, security, acknowledgeHandler(broker)))
	return security.limitConcurrency(mux)
}

func newIngestMux(broker *events.Broker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(broker))
	mux.HandleFunc("POST /v1/events", publishHandler(broker))
	return mux
}

func publishHandler(broker *events.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()

		var input events.Input
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if input.Kind == "" || input.Title == "" {
			http.Error(w, "kind and title are required", http.StatusBadRequest)
			return
		}

		event, err := broker.Publish(r.Context(), input)
		if err != nil {
			slog.Error("failed to persist event", "error", err)
			http.Error(w, "failed to persist event", http.StatusInternalServerError)
			return
		}
		slog.Info("event accepted",
			"event_id", event.ID,
			"kind", event.Kind,
			"agent", event.Agent,
			"remote_addr", r.RemoteAddr,
		)
		writeJSON(w, http.StatusCreated, event)
	}
}

func healthHandler(broker *events.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := broker.Healthy(r.Context()); err != nil {
			http.Error(w, "event journal is unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type deviceContextKey struct{}

type authenticatedDevice struct {
	ID        string
	TokenHash [32]byte
}

func pairHandler(broker *events.Broker) http.HandlerFunc {
	type pairingRequest struct {
		Secret   string `json:"secret"`
		DeviceID string `json:"device_id"`
		Name     string `json:"name"`
	}
	type pairingResponse struct {
		DeviceID string `json:"device_id"`
		Token    string `json:"token"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		var request pairingRequest
		if err := decoder.Decode(&request); err != nil ||
			!deviceIDPattern.MatchString(request.DeviceID) || len(request.Name) > 128 {
			slog.Warn("rejected malformed pairing request", "remote_addr", r.RemoteAddr)
			http.Error(w, "valid secret and device_id are required", http.StatusBadRequest)
			return
		}
		secret, err := base64.RawURLEncoding.DecodeString(request.Secret)
		if err != nil || len(secret) != 32 {
			slog.Warn("rejected invalid pairing code", "device_id", request.DeviceID, "remote_addr", r.RemoteAddr)
			http.Error(w, "pairing code is invalid or expired", http.StatusUnauthorized)
			return
		}
		secretHash := sha256.Sum256(secret)
		token := make([]byte, 32)
		if _, err := rand.Read(token); err != nil {
			slog.Error("failed to generate device credential", "error", err)
			http.Error(w, "failed to create device credential", http.StatusInternalServerError)
			return
		}
		tokenHash := sha256.Sum256(token)
		if err := broker.RedeemPairingCode(
			r.Context(), secretHash[:], request.DeviceID, request.Name, tokenHash[:],
		); err != nil {
			if errors.Is(err, events.ErrInvalidPairingCode) {
				slog.Warn("rejected invalid or expired pairing code", "device_id", request.DeviceID, "remote_addr", r.RemoteAddr)
				http.Error(w, "pairing code is invalid or expired", http.StatusUnauthorized)
				return
			}
			slog.Error("failed to redeem pairing code", "error", err)
			http.Error(w, "failed to pair device", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		slog.Info("paired device",
			"device_id", request.DeviceID,
			"name", request.Name,
			"remote_addr", r.RemoteAddr,
		)
		writeJSON(w, http.StatusCreated, pairingResponse{
			DeviceID: request.DeviceID,
			Token:    base64.RawURLEncoding.EncodeToString(token),
		})
	}
}

func authenticateDevice(broker *events.Broker, security *publicSecurity, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := parseBearerToken(r.Header.Get("Authorization"))
		if !ok {
			if !security.authFailures.allowRemote(r.RemoteAddr) {
				writeRateLimited(w)
				return
			}
			slog.Warn("rejected unauthenticated device request",
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
			)
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "device authentication required", http.StatusUnauthorized)
			return
		}
		if !security.authFailures.readyRemote(r.RemoteAddr) ||
			!security.authAttempts.allowRemote(r.RemoteAddr) {
			writeRateLimited(w)
			return
		}
		tokenHash := sha256.Sum256(token)
		deviceID, err := broker.AuthenticateDevice(r.Context(), tokenHash[:])
		if err != nil {
			if errors.Is(err, events.ErrUnauthorized) {
				if !security.authFailures.allowRemote(r.RemoteAddr) {
					writeRateLimited(w)
					return
				}
				slog.Warn("rejected invalid device credential",
					"method", r.Method,
					"path", r.URL.Path,
					"remote_addr", r.RemoteAddr,
				)
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "device credential is invalid or revoked", http.StatusUnauthorized)
				return
			}
			slog.Error("failed to authenticate device", "error", err)
			http.Error(w, "failed to authenticate device", http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), deviceContextKey{}, authenticatedDevice{
			ID: deviceID, TokenHash: tokenHash,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func parseBearerToken(header string) ([]byte, bool) {
	scheme, encoded, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || encoded == "" || strings.Contains(encoded, " ") {
		return nil, false
	}
	token, err := base64.RawURLEncoding.DecodeString(encoded)
	return token, err == nil && len(token) == 32
}

func acknowledgeHandler(broker *events.Broker) http.HandlerFunc {
	type acknowledgement struct {
		DeviceID string `json:"device_id"`
		Through  uint64 `json:"through"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		var ack acknowledgement
		device, _ := r.Context().Value(deviceContextKey{}).(authenticatedDevice)
		if err := decoder.Decode(&ack); err != nil || ack.DeviceID != device.ID {
			http.Error(w, "valid device_id and through are required", http.StatusBadRequest)
			return
		}
		if err := broker.Acknowledge(r.Context(), ack.DeviceID, ack.Through); err != nil {
			slog.Error("failed to persist acknowledgement", "error", err)
			http.Error(w, "failed to persist acknowledgement", http.StatusInternalServerError)
			return
		}
		slog.Info("event cursor acknowledged",
			"device_id", ack.DeviceID,
			"through", ack.Through,
			"remote_addr", r.RemoteAddr,
		)
		w.WriteHeader(http.StatusNoContent)
	}
}

func streamHandler(broker *events.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 3 {
			http.Error(w, "HTTP/3 is required", http.StatusHTTPVersionNotSupported)
			return
		}

		after, err := parseCursor(r.URL.Query().Get("after"))
		if err != nil {
			http.Error(w, "after must be an unsigned integer", http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
			return
		}

		replay, stream, err := broker.Subscribe(r.Context(), after)
		if err != nil {
			slog.Error("failed to replay events", "error", err)
			http.Error(w, "event journal is unavailable", http.StatusInternalServerError)
			return
		}
		device, _ := r.Context().Value(deviceContextKey{}).(authenticatedDevice)
		connectedAt := time.Now()
		disconnectReason := "stream ended"
		var disconnectErr error
		eventsSent := 0
		heartbeatsSent := 0
		slog.Info("device event stream connected",
			"device_id", device.ID,
			"remote_addr", r.RemoteAddr,
			"after", after,
			"replay_events", len(replay),
			"active_streams", broker.SubscriberCount(),
		)
		defer func() {
			attributes := []any{
				"device_id", device.ID,
				"remote_addr", r.RemoteAddr,
				"reason", disconnectReason,
				"duration", time.Since(connectedAt).Round(time.Millisecond),
				"events_sent", eventsSent,
				"heartbeats_sent", heartbeatsSent,
			}
			if disconnectErr != nil {
				attributes = append(attributes, "error", disconnectErr)
			}
			slog.Info("device event stream disconnected", attributes...)
		}()

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for _, event := range replay {
			if !deviceStillAuthorized(r.Context(), broker) {
				disconnectReason = "credential revoked"
				return
			}
			if err := writeLine(w, flusher, event); err != nil {
				disconnectReason = "write failed"
				disconnectErr = err
				return
			}
			eventsSent++
		}
		heartbeat := time.NewTicker(30 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				disconnectReason = "client disconnected"
				disconnectErr = context.Cause(r.Context())
				return
			case event, open := <-stream:
				if !open {
					disconnectReason = "subscriber closed"
					return
				}
				if !deviceStillAuthorized(r.Context(), broker) {
					disconnectReason = "credential revoked"
					return
				}
				if err := writeLine(w, flusher, event); err != nil {
					disconnectReason = "write failed"
					disconnectErr = err
					return
				}
				eventsSent++
			case now := <-heartbeat.C:
				if !deviceStillAuthorized(r.Context(), broker) {
					disconnectReason = "credential revoked"
					return
				}
				if err := writeLine(w, flusher, map[string]any{
					"type": "heartbeat",
					"time": now.UTC(),
				}); err != nil {
					disconnectReason = "write failed"
					disconnectErr = err
					return
				}
				heartbeatsSent++
			}
		}
	}
}

func deviceStillAuthorized(ctx context.Context, broker *events.Broker) bool {
	device, ok := ctx.Value(deviceContextKey{}).(authenticatedDevice)
	if !ok {
		return false
	}
	deviceID, err := broker.AuthenticateDevice(ctx, device.TokenHash[:])
	return err == nil && deviceID == device.ID
}

func parseCursor(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func writeLine(w http.ResponseWriter, flusher http.Flusher, value any) error {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		slog.Warn("failed to write response", "error", err)
	}
}
