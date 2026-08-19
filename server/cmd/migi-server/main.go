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
	"strings"
	"syscall"
	"time"

	"github.com/mirmik/migi/server/internal/admin"
	"github.com/mirmik/migi/server/internal/apkinspect"
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
	artifactDirectoryDefault := os.Getenv("MIGI_ARTIFACT_DIRECTORY")
	if artifactDirectoryDefault == "" {
		artifactDirectoryDefault = "migi-artifacts"
	}
	transferDirectoryDefault := os.Getenv("MIGI_FILE_DIRECTORY")
	if transferDirectoryDefault == "" {
		transferDirectoryDefault = "migi-files"
	}
	mediaDirectoryDefault := os.Getenv("MIGI_MEDIA_DIRECTORY")
	if mediaDirectoryDefault == "" {
		mediaDirectoryDefault = "migi-media"
	}
	listen := flag.String("listen", ":8443", "UDP address for the HTTP/3 server")
	ingestListen := flag.String("ingest-listen", "127.0.0.1:8787", "trusted local TCP address for event submission")
	agentListen := flag.String("agent-listen", "", "TLS/TCP address for authenticated agent events, files, and media; empty disables it")
	adminListen := flag.String("admin-listen", "127.0.0.1:8788", "local TCP address for the administration UI; empty disables it")
	publicEndpoint := flag.String("public-endpoint", "", "default public https://host[:port] for pairing invitations")
	agentEndpoint := flag.String("agent-endpoint", "", "public https://host[:port] advertised to agent hooks")
	databasePath := flag.String("db", "migi.db", "SQLite event journal path")
	artifactDirectory := flag.String("artifact-dir", artifactDirectoryDefault, "immutable APK artifact directory")
	artifactMaxBytes := flag.Int64("artifact-max-bytes", defaultMaxAPKBytes, "maximum bytes per uploaded APK")
	artifactTotalBytes := flag.Int64("artifact-total-bytes", defaultMaxArtifactBytes, "maximum total artifact bytes before publication stops")
	transferDirectory := flag.String("file-dir", transferDirectoryDefault, "shared file directory")
	transferMaxBytes := flag.Int64("file-max-bytes", defaultTransferMaxBytes, "maximum bytes per shared file")
	transferTotalBytes := flag.Int64("file-total-bytes", defaultTransferTotalBytes, "maximum total shared file bytes")
	transferTTL := flag.Duration("file-ttl", defaultTransferTTL, "shared file retention period")
	mediaDirectory := flag.String("media-dir", mediaDirectoryDefault, "private agent media directory")
	mediaMaxBytes := flag.Int64("media-max-bytes", defaultMediaMaxBytes, "maximum bytes per media object")
	mediaTotalBytes := flag.Int64("media-total-bytes", defaultMediaTotalBytes, "maximum total media bytes")
	mediaTTL := flag.Duration("media-ttl", defaultMediaTTL, "unreferenced directly uploaded media retention period")
	apksignerPath := flag.String("apksigner", os.Getenv("MIGI_APKSIGNER"), "path to pinned Android build-tools apksigner; empty disables release delivery")
	aapt2Path := flag.String("aapt2", os.Getenv("MIGI_AAPT2"), "path to pinned Android build-tools aapt2; empty disables release delivery")
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
	transfers, err := newTransferStore(
		broker, *transferDirectory, *transferMaxBytes, *transferTotalBytes, *transferTTL,
	)
	if err != nil {
		return fmt.Errorf("configure shared file storage: %w", err)
	}
	slog.Info("shared file exchange enabled",
		"directory", transfers.root,
		"max_file_bytes", transfers.maxBytes,
		"max_total_bytes", transfers.totalBytes,
		"ttl", transfers.ttl,
	)
	media, err := newMediaStore(
		broker, *mediaDirectory, *mediaMaxBytes, *mediaTotalBytes, *mediaTTL,
	)
	if err != nil {
		return fmt.Errorf("configure media storage: %w", err)
	}
	slog.Info("private media storage enabled",
		"directory", media.root,
		"max_object_bytes", media.maxBytes,
		"max_total_bytes", media.totalBytes,
		"ttl", media.ttl,
	)
	var releases *releaseStore
	if *apksignerPath != "" || *aapt2Path != "" {
		inspector, err := apkinspect.New(apkinspect.Config{
			APKSIGNER: *apksignerPath,
			AAPT2:     *aapt2Path,
		})
		if err != nil {
			return fmt.Errorf("configure APK inspector: %w", err)
		}
		releases, err = newReleaseStore(
			broker, inspector, *artifactDirectory, *artifactMaxBytes, *artifactTotalBytes,
		)
		if err != nil {
			return fmt.Errorf("configure artifact storage: %w", err)
		}
		versions, err := inspector.Versions(context.Background())
		if err != nil {
			return fmt.Errorf("read APK verifier versions: %w", err)
		}
		slog.Info("release delivery enabled",
			"artifact_directory", releases.root,
			"max_apk_bytes", releases.maxAPKBytes,
			"max_total_bytes", releases.maxTotalBytes,
			"verifier", versions,
		)
	} else {
		slog.Info("release delivery disabled; configure both -apksigner and -aapt2 to enable it")
	}
	startedAt := time.Now()
	fingerprint, err := certificateFingerprint(*cert)
	if err != nil {
		return err
	}
	slog.Info("server configured",
		"public_endpoint", *publicEndpoint,
		"agent_endpoint", *agentEndpoint,
		"database", *databasePath,
		"certificate_fingerprint", fingerprint,
	)
	if *publicEndpoint == "" {
		slog.Warn("default public endpoint is not configured; enter one in the administration UI when pairing")
	}

	publicSecurity := newPublicSecurity()
	publicMux := newPublicMuxWithAllStores(broker, releases, transfers, media, publicSecurity)
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
		Handler:           newIngestMuxWithStores(broker, transfers, media),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	var agentServer *http.Server
	if *agentListen != "" {
		agentServer = &http.Server{
			Addr:              *agentListen,
			Handler:           newAgentMuxWithAllStores(broker, releases, transfers, media, newAgentSecurity()),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       30 * time.Second,
			MaxHeaderBytes:    16 << 10,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{tlsCertificate},
				MinVersion:   tls.VersionTLS12,
			},
		}
	}
	var adminServer *http.Server
	if *adminListen != "" {
		adminHandler, err := admin.New(admin.Config{
			Broker:                 broker,
			Files:                  transfers,
			PublicEndpoint:         *publicEndpoint,
			CertificateFingerprint: fingerprint,
			PublicListen:           *listen,
			IngestListen:           *ingestListen,
			AgentListen:            *agentListen,
			AgentEndpoint:          *agentEndpoint,
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

	serverErrors := make(chan error, 4)
	go func() {
		slog.Info("starting trusted local ingest", "address", *ingestListen)
		serverErrors <- ingestServer.ListenAndServe()
	}()
	go func() {
		slog.Info("starting public HTTP/3 server", "address", *listen)
		serverErrors <- quicServer.ServeListener(quicListener)
	}()
	if agentServer != nil {
		go func() {
			slog.Info("starting authenticated agent HTTPS ingress", "address", *agentListen)
			serverErrors <- agentServer.ListenAndServeTLS("", "")
		}()
	}
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
	var agentErr error
	if agentServer != nil {
		agentErr = agentServer.Shutdown(shutdownContext)
	}
	return errors.Join(serveErr, ingestErr, quicErr, agentErr, adminErr)
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
	return newPublicMuxWithReleases(broker, nil, security)
}

func newPublicMuxWithReleases(broker *events.Broker, releases *releaseStore, security *publicSecurity) http.Handler {
	return newPublicMuxWithStores(broker, releases, nil, security)
}

func newPublicMuxWithStores(
	broker *events.Broker,
	releases *releaseStore,
	transfers *transferStore,
	security *publicSecurity,
) http.Handler {
	return newPublicMuxWithAllStores(broker, releases, transfers, nil, security)
}

func newPublicMuxWithAllStores(
	broker *events.Broker,
	releases *releaseStore,
	transfers *transferStore,
	media *mediaStore,
	security *publicSecurity,
) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", security.rateLimit("health", security.healthChecks, healthHandler(broker)))
	mux.Handle("POST /v1/pair", security.rateLimit("pair", security.pairRequests, pairHandler(broker)))
	mux.Handle("GET /v1/events", authenticateDevice(broker, security, security.limitDeviceStreams(streamHandler(broker))))
	mux.Handle("POST /v1/ack", authenticateDevice(broker, security, acknowledgeHandler(broker)))
	if releases != nil {
		mux.Handle("GET /v1/releases/{artifactID}", authenticateDevice(broker, security, releases.releaseHandler(false)))
		mux.Handle("GET /v1/releases/{artifactID}/apk", authenticateDevice(broker, security, releases.releaseHandler(true)))
	}
	if transfers != nil {
		transfers.routes(mux, func(next http.Handler) http.Handler {
			return authenticateDevice(broker, security, next)
		}, func(r *http.Request) string {
			device, _ := r.Context().Value(deviceContextKey{}).(authenticatedDevice)
			return "device:" + device.ID
		})
	}
	if media != nil {
		media.deviceRoutes(mux, func(next http.Handler) http.Handler {
			return authenticateDevice(broker, security, next)
		})
	}
	return security.limitConcurrency(mux)
}

func newIngestMux(broker *events.Broker) http.Handler {
	return newIngestMuxWithTransfers(broker, nil)
}

func newIngestMuxWithTransfers(broker *events.Broker, transfers *transferStore) http.Handler {
	return newIngestMuxWithStores(broker, transfers, nil)
}

func newIngestMuxWithStores(
	broker *events.Broker,
	transfers *transferStore,
	media *mediaStore,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(broker))
	mux.HandleFunc("POST /v1/events", publishHandler(broker))
	if transfers != nil {
		transfers.routes(mux, func(next http.Handler) http.Handler { return next }, func(r *http.Request) string {
			source := r.Header.Get("X-Migi-Source")
			if source == "" {
				return "agent"
			}
			return "agent:" + source
		})
	}
	if media != nil {
		media.agentRoutes(mux, func(next http.Handler) http.Handler { return next }, func(r *http.Request) string {
			return r.Header.Get("X-Migi-Source")
		})
	}
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
		if input.Kind == playbackQueueEventKind {
			http.Error(w, "media.queue.set must use /v1/playback/queue", http.StatusBadRequest)
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
			if errors.Is(err, events.ErrInvalidAcknowledgement) {
				http.Error(w, "acknowledgement exceeds the event journal", http.StatusBadRequest)
				return
			}
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

		device, _ := r.Context().Value(deviceContextKey{}).(authenticatedDevice)
		after, err := broker.Acknowledged(r.Context(), device.ID)
		if err != nil {
			slog.Error("failed to read device acknowledgement", "device_id", device.ID, "error", err)
			http.Error(w, "event journal is unavailable", http.StatusInternalServerError)
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
		connectedAt := time.Now()
		disconnectReason := "stream ended"
		var disconnectErr error
		eventsSent := 0
		heartbeatsSent := 0
		slog.Info("device event stream connected",
			"device_id", device.ID,
			"remote_addr", r.RemoteAddr,
			"after", after,
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

		cursor := after
		for {
			for _, event := range replay {
				if !deviceStillAuthorized(r.Context(), broker) {
					disconnectReason = "credential revoked"
					return
				}
				event, err = eventForDevice(r.Context(), broker, device.ID, event)
				if err != nil {
					disconnectReason = "release authorization failed"
					disconnectErr = err
					return
				}
				if err := writeLine(w, flusher, event); err != nil {
					disconnectReason = "write failed"
					disconnectErr = err
					return
				}
				cursor = event.ID
				eventsSent++
			}
			if stream != nil {
				break
			}
			replay, stream, err = broker.Subscribe(r.Context(), cursor)
			if err != nil {
				disconnectReason = "replay failed"
				disconnectErr = err
				return
			}
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
				event, err = eventForDevice(r.Context(), broker, device.ID, event)
				if err != nil {
					disconnectReason = "release authorization failed"
					disconnectErr = err
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

func eventForDevice(
	ctx context.Context,
	broker *events.Broker,
	deviceID string,
	event events.Event,
) (events.Event, error) {
	if event.Artifact == nil {
		return event, nil
	}
	if _, err := broker.ReleaseForDevice(ctx, deviceID, event.Artifact.ID); err != nil {
		if errors.Is(err, events.ErrReleaseNotFound) {
			return events.Event{
				ID:        event.ID,
				Kind:      "internal.filtered",
				Title:     "Filtered event",
				CreatedAt: event.CreatedAt,
			}, nil
		}
		return events.Event{}, err
	}
	return event, nil
}

func deviceStillAuthorized(ctx context.Context, broker *events.Broker) bool {
	device, ok := ctx.Value(deviceContextKey{}).(authenticatedDevice)
	if !ok {
		return false
	}
	deviceID, err := broker.AuthenticateDevice(ctx, device.TokenHash[:])
	return err == nil && deviceID == device.ID
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
